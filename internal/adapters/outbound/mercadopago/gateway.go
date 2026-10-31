// Package mercadopago é o adapter da porta PaymentGateway para o Mercado Pago (Checkout Pro via
// Preferences API), falando HTTP direto com a API — sem SDK — através de pkg/httpclient
// (timeout por tentativa, retry com backoff só para métodos idempotentes e circuit breaker).
//
// Referência: tech-challenge/fase4-notes/mercadopago.md (verificada na documentação oficial).
//
//	criar cobrança   POST /checkout/preferences           → id, init_point
//	consultar        GET  /v1/payments/{id}               → status, transaction_amount, external_reference
//	estornar         POST /v1/payments/{id}/refunds       (X-Idempotency-Key obrigatório, corpo vazio = total)
//	cancelar         PUT  /v1/payments/{id} {"status":"cancelled"}
package mercadopago

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/httpclient"
)

// DefaultBaseURL é a base da API do Mercado Pago.
const DefaultBaseURL = "https://api.mercadopago.com"

// mpTimeLayout é o formato ISO 8601 com milissegundos e fuso que a API usa nas datas da preferência.
const mpTimeLayout = "2006-01-02T15:04:05.000-07:00"

// Config configura o adapter.
type Config struct {
	BaseURL         string        // MP_BASE_URL
	AccessToken     string        // MP_ACCESS_TOKEN (no Checkout Pro de teste começa com APP_USR)
	NotificationURL string        // MP_NOTIFICATION_URL: HTTPS público do webhook (rota do API Gateway)
	PreferenceTTL   time.Duration // vigência do link de pagamento
}

// Gateway implementa ports.PaymentGateway.
type Gateway struct {
	cfg    Config
	client *httpclient.Client
	now    func() time.Time
}

// New cria o adapter. client já vem configurado com timeout, retry e circuit breaker.
func New(cfg Config, client *httpclient.Client) *Gateway {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.PreferenceTTL <= 0 {
		cfg.PreferenceTTL = 24 * time.Hour
	}
	return &Gateway{cfg: cfg, client: client, now: time.Now}
}

var _ ports.PaymentGateway = (*Gateway)(nil)

func (g *Gateway) Provider() entities.Provider { return entities.ProviderMercadoPago }

type preferenceItem struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Quantity   int     `json:"quantity"`
	CurrencyID string  `json:"currency_id"`
	UnitPrice  float64 `json:"unit_price"`
}

type preferenceRequest struct {
	Items              []preferenceItem `json:"items"`
	ExternalReference  string           `json:"external_reference"`
	NotificationURL    string           `json:"notification_url,omitempty"`
	Expires            bool             `json:"expires"`
	ExpirationDateFrom string           `json:"expiration_date_from"`
	ExpirationDateTo   string           `json:"expiration_date_to"`
	BinaryMode         bool             `json:"binary_mode"`
}

type preferenceResponse struct {
	ID        string `json:"id"`
	InitPoint string `json:"init_point"`
}

// CreateCheckout cria a preferência do Checkout Pro. O orçamento vira um único item com o total
// (evita divergência de arredondamento entre itens) e o external_reference é o ID do nosso
// pagamento — é por ele que o webhook volta ao registro certo. O link devolvido é o init_point
// (a referência da API manda não usar sandbox_init_point, nem em teste).
func (g *Gateway) CreateCheckout(ctx context.Context, req ports.CheckoutRequest) (string, string, error) {
	from := g.now().UTC()
	body := preferenceRequest{
		Items: []preferenceItem{{
			ID:         req.BudgetID,
			Title:      fmt.Sprintf("Orçamento da OS #%d", req.OSCode),
			Quantity:   1,
			CurrencyID: "BRL",
			UnitPrice:  entities.RoundMoney(req.Amount),
		}},
		ExternalReference:  req.PaymentID,
		NotificationURL:    g.cfg.NotificationURL,
		Expires:            true,
		ExpirationDateFrom: from.Format(mpTimeLayout),
		ExpirationDateTo:   from.Add(g.cfg.PreferenceTTL).Format(mpTimeLayout),
		BinaryMode:         false,
	}
	var out preferenceResponse
	if err := g.call(ctx, http.MethodPost, "/checkout/preferences", body, nil, &out); err != nil {
		return "", "", err
	}
	if out.ID == "" || out.InitPoint == "" {
		return "", "", fmt.Errorf("%w: preferência sem id ou init_point", ports.ErrGatewayUnavailable)
	}
	return out.ID, out.InitPoint, nil
}

type paymentResponse struct {
	ID                flexibleID `json:"id"`
	Status            string     `json:"status"`
	StatusDetail      string     `json:"status_detail"`
	TransactionAmount float64    `json:"transaction_amount"`
	ExternalReference string     `json:"external_reference"`
}

// GetPayment relê o pagamento: é a fonte da verdade depois de um webhook.
func (g *Gateway) GetPayment(ctx context.Context, providerPaymentID string) (ports.ProviderPayment, error) {
	var out paymentResponse
	if err := g.call(ctx, http.MethodGet, "/v1/payments/"+url.PathEscape(providerPaymentID), nil, nil, &out); err != nil {
		return ports.ProviderPayment{}, err
	}
	return ports.ProviderPayment{
		ID:                string(out.ID),
		Status:            ports.ProviderStatus(out.Status),
		StatusDetail:      out.StatusDetail,
		Amount:            out.TransactionAmount,
		ExternalReference: out.ExternalReference,
	}, nil
}

// Refund estorna o valor total. A X-Idempotency-Key é obrigatória e se repete nas novas
// tentativas da mesma operação lógica, garantindo um único estorno.
func (g *Gateway) Refund(ctx context.Context, providerPaymentID, idempotencyKey string) error {
	if idempotencyKey == "" {
		return fmt.Errorf("%w: X-Idempotency-Key obrigatória no estorno", ports.ErrGatewayRejected)
	}
	path := "/v1/payments/" + url.PathEscape(providerPaymentID) + "/refunds"
	return g.call(ctx, http.MethodPost, path, struct{}{}, map[string]string{"X-Idempotency-Key": idempotencyKey}, nil)
}

// Cancel cancela um pagamento ainda pending/in_process/authorized.
func (g *Gateway) Cancel(ctx context.Context, providerPaymentID string) error {
	path := "/v1/payments/" + url.PathEscape(providerPaymentID)
	return g.call(ctx, http.MethodPut, path, map[string]string{"status": "cancelled"}, nil, nil)
}

// call executa a requisição e traduz o resultado para os erros da porta.
func (g *Gateway) call(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := g.client.Do(ctx, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, method, g.cfg.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.cfg.AccessToken)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return req, nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("%w: %s %s: %w", ports.ErrGatewayUnavailable, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(method, path, resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("%w: resposta inválida de %s %s: %v", ports.ErrGatewayUnavailable, method, path, err)
	}
	return nil
}

type apiError struct {
	Message string `json:"message"`
	Error   string `json:"error"`
}

// statusError classifica a resposta de erro. 5xx e 429 já chegam como erro do httpclient.
//   - 404: recurso inexistente;
//   - 401/403: credencial inválida — problema de configuração, não do pagamento: transitório,
//     para a mensagem ir à DLQ em vez de cancelar a OS do cliente;
//   - demais 4xx: recusa definitiva (dados inválidos, estado que não permite a operação).
func statusError(method, path string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var e apiError
	_ = json.Unmarshal(raw, &e)
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = strings.TrimSpace(e.Error)
	}
	base := fmt.Sprintf("%s %s: status %d", method, path, resp.StatusCode)
	if detail != "" {
		base += ": " + detail
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ports.ErrGatewayNotFound, base)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: credencial rejeitada: %s", ports.ErrGatewayUnavailable, base)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return fmt.Errorf("%w: %s", ports.ErrGatewayRejected, base)
	default:
		return fmt.Errorf("%w: %s", ports.ErrGatewayUnavailable, base)
	}
}

// flexibleID aceita o id do pagamento como número (forma da API) ou como string.
type flexibleID string

func (f *flexibleID) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexibleID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexibleID(n.String())
	return nil
}
