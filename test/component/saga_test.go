//go:build component

package component

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/config"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/mercadopago"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/google/uuid"
)

// Caminho feliz com o gateway fake: orçamento → aprovação pelo cliente → link → pagamento
// aprovado (simulate) → estorno de compensação. Também cobre idempotência de comando e de os_id.
func TestSaga_CaminhoFelizComGatewayFake(t *testing.T) {
	s := startService(t, config.GatewayFake, nil)
	osID := "os-" + uuid.NewString()
	client, admin := token(t, "client", ownerDocument), token(t, "admin", "")

	// CreateBudget → BudgetCreated.
	cmd := createBudget(osID, "")
	env, _ := contracts.NewEnvelope(contracts.CmdCreateBudget, "os-service", contracts.Meta{SagaID: "saga-" + osID, OSID: osID, CorrelationID: "corr-" + osID}, cmd)
	s.sendEnvelope(t, env)
	created := s.events.await(t, contracts.EvtBudgetCreated, osID)
	budget := decode[contracts.BudgetCreated](t, created)
	if budget.Total != 180.5 || budget.BudgetID == "" || created.CausationID != env.MessageID || created.CorrelationID != "corr-"+osID || created.Producer != "billing-service" {
		t.Fatalf("BudgetCreated: %+v %+v", created, budget)
	}

	// Mesma mensagem reentregue: o inbox descarta. Outro CreateBudget da mesma OS: idempotente
	// por os_id, republica o mesmo orçamento.
	s.sendEnvelope(t, env)
	s.send(t, contracts.CmdCreateBudget, osID, cmd)
	again := decode[contracts.BudgetCreated](t, s.events.await(t, contracts.EvtBudgetCreated, osID))
	if again.BudgetID != budget.BudgetID {
		t.Fatalf("CreateBudget repetido criou outro orçamento: %s ≠ %s", again.BudgetID, budget.BudgetID)
	}

	// Consulta (API composition do os-service) e regra de dono.
	status, body := s.http(t, "GET", "/api/v1/budgets/os/"+osID, admin, "")
	if status != 200 || body["status"] != "pending_approval" || body["total"] != 180.5 {
		t.Fatalf("GET por OS: %d %v", status, body)
	}
	if status, _ := s.http(t, "POST", "/api/v1/budgets/"+budget.BudgetID+"/approve", token(t, "client", "11144477735"), ""); status != 403 {
		t.Fatalf("outro cliente aprovou: %d", status)
	}

	// Aprovação do cliente → BudgetApproved.
	if status, body := s.http(t, "POST", "/api/v1/budgets/"+budget.BudgetID+"/approve", client, ""); status != 200 || body["status"] != "approved" {
		t.Fatalf("aprovar: %d %v", status, body)
	}
	if decode[contracts.BudgetApproved](t, s.events.await(t, contracts.EvtBudgetApproved, osID)).BudgetID != budget.BudgetID {
		t.Fatal("BudgetApproved de outro orçamento")
	}

	// RequestPayment → PaymentLinkCreated com a página de checkout fake.
	s.send(t, contracts.CmdRequestPayment, osID, contracts.RequestPayment{BudgetID: budget.BudgetID})
	link := decode[contracts.PaymentLinkCreated](t, s.events.await(t, contracts.EvtPaymentLinkCreated, osID))
	if !strings.HasPrefix(link.CheckoutURL, s.baseURL+"/fake-checkout/") || link.PaymentID == "" {
		t.Fatalf("link: %+v", link)
	}
	if resp, err := http.Get(link.CheckoutURL); err != nil || resp.StatusCode != 200 {
		t.Fatalf("página de checkout fake: %v", err)
	} else {
		_ = resp.Body.Close()
	}

	// Retorno do provedor simulado → PaymentApproved.
	if status, body := s.http(t, "POST", "/api/v1/payments/"+link.PaymentID+"/simulate", admin, `{"status":"approved"}`); status != 200 || body["status"] != "approved" {
		t.Fatalf("simulate: %d %v", status, body)
	}
	approved := decode[contracts.PaymentApproved](t, s.events.await(t, contracts.EvtPaymentApproved, osID))
	if approved.PaymentID != link.PaymentID || approved.Amount != 180.5 {
		t.Fatalf("PaymentApproved: %+v", approved)
	}

	// Compensação: RefundPayment → PaymentRefunded e pagamento refunded.
	s.send(t, contracts.CmdRefundPayment, osID, contracts.RefundPayment{BudgetID: budget.BudgetID, Reason: "execução falhou"})
	if decode[contracts.PaymentRefunded](t, s.events.await(t, contracts.EvtPaymentRefunded, osID)).PaymentID != link.PaymentID {
		t.Fatal("PaymentRefunded de outro pagamento")
	}
	if status, body := s.http(t, "GET", "/api/v1/payments/"+link.PaymentID, client, ""); status != 200 || body["status"] != "refunded" {
		t.Fatalf("pagamento depois do estorno: %d %v", status, body)
	}
}

// Recusa do cliente, cancelamento pela saga e falhas simuladas (fault injection).
func TestSaga_RecusaCancelamentoEFalhasSimuladas(t *testing.T) {
	s := startService(t, config.GatewayFake, nil)
	client := token(t, "client", ownerDocument)

	t.Run("cliente recusa", func(t *testing.T) {
		osID := "os-" + uuid.NewString()
		s.send(t, contracts.CmdCreateBudget, osID, createBudget(osID, ""))
		b := decode[contracts.BudgetCreated](t, s.events.await(t, contracts.EvtBudgetCreated, osID))
		if status, _ := s.http(t, "POST", "/api/v1/budgets/"+b.BudgetID+"/reject", client, `{}`); status != 400 {
			t.Fatalf("recusa sem motivo: %d", status)
		}
		if status, _ := s.http(t, "POST", "/api/v1/budgets/"+b.BudgetID+"/reject", client, `{"reason":"muito caro"}`); status != 200 {
			t.Fatalf("recusa: %d", status)
		}
		if decode[contracts.BudgetRejected](t, s.events.await(t, contracts.EvtBudgetRejected, osID)).Reason != "muito caro" {
			t.Fatal("motivo da recusa não foi no evento")
		}
	})

	t.Run("saga cancela com pagamento pendente", func(t *testing.T) {
		osID := "os-" + uuid.NewString()
		s.send(t, contracts.CmdCreateBudget, osID, createBudget(osID, ""))
		b := decode[contracts.BudgetCreated](t, s.events.await(t, contracts.EvtBudgetCreated, osID))
		s.http(t, "POST", "/api/v1/budgets/"+b.BudgetID+"/approve", client, "")
		s.events.await(t, contracts.EvtBudgetApproved, osID)
		s.send(t, contracts.CmdRequestPayment, osID, contracts.RequestPayment{BudgetID: b.BudgetID})
		link := decode[contracts.PaymentLinkCreated](t, s.events.await(t, contracts.EvtPaymentLinkCreated, osID))

		s.send(t, contracts.CmdCancelBudget, osID, contracts.CancelBudget{BudgetID: b.BudgetID, Reason: "prazo de pagamento expirou"})
		s.events.await(t, contracts.EvtBudgetCancelled, osID)
		if _, body := s.http(t, "GET", "/api/v1/payments/"+link.PaymentID, client, ""); body["status"] != "cancelled" {
			t.Fatalf("pagamento pendente deveria ser cancelado junto: %v", body)
		}
		// O link cancelado não pode mais ser pago.
		if status, _ := s.http(t, "POST", "/api/v1/payments/"+link.PaymentID+"/simulate", token(t, "admin", ""), `{"status":"approved"}`); status != 409 {
			t.Fatalf("pagar link cancelado: %d", status)
		}
	})

	t.Run("falha simulada na criação do orçamento", func(t *testing.T) {
		osID := "os-" + uuid.NewString()
		s.send(t, contracts.CmdCreateBudget, osID, createBudget(osID, "budget"))
		if decode[contracts.BudgetCreationFailed](t, s.events.await(t, contracts.EvtBudgetCreationFailed, osID)).Reason == "" {
			t.Fatal("BudgetCreationFailed sem motivo")
		}
		if status, _ := s.http(t, "GET", "/api/v1/budgets/os/"+osID, token(t, "admin", ""), ""); status != 404 {
			t.Fatalf("orçamento não deveria existir: %d", status)
		}
	})

	t.Run("falha simulada no pagamento", func(t *testing.T) {
		osID := "os-" + uuid.NewString()
		s.send(t, contracts.CmdCreateBudget, osID, createBudget(osID, ""))
		b := decode[contracts.BudgetCreated](t, s.events.await(t, contracts.EvtBudgetCreated, osID))
		s.http(t, "POST", "/api/v1/budgets/"+b.BudgetID+"/approve", client, "")
		s.events.await(t, contracts.EvtBudgetApproved, osID)
		s.send(t, contracts.CmdRequestPayment, osID, contracts.RequestPayment{BudgetID: b.BudgetID, SimulateFailure: "payment"})
		if decode[contracts.PaymentFailed](t, s.events.await(t, contracts.EvtPaymentFailed, osID)).Reason == "" {
			t.Fatal("PaymentFailed sem motivo")
		}
	})
}

// fakeMercadoPago imita a API do Mercado Pago (preferência, consulta e estorno).
type fakeMercadoPago struct {
	mu          sync.Mutex
	preferences map[string]string // payment id (external_reference) → valor
	refundKeys  []string
	server      *httptest.Server
}

func newFakeMercadoPago(t *testing.T) *fakeMercadoPago {
	f := &fakeMercadoPago{preferences: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkout/preferences", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer APP_USR-component" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			ExternalReference string `json:"external_reference"`
			NotificationURL   string `json:"notification_url"`
			Items             []struct {
				UnitPrice float64 `json:"unit_price"`
			} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.preferences[body.ExternalReference] = fmt.Sprint(body.Items[0].UnitPrice)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"pref-%s","init_point":"https://www.mercadopago.com.br/checkout/v1/redirect?pref_id=pref-%s"}`, body.ExternalReference, body.ExternalReference)
	})
	// O id do pagamento no MP é "9" + external_reference sem hífens — determinístico para o teste.
	mux.HandleFunc("GET /v1/payments/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		for ref, amount := range f.preferences {
			if mpID(ref) == id {
				_, _ = fmt.Fprintf(w, `{"id":%s,"status":"approved","status_detail":"accredited","transaction_amount":%s,"external_reference":"%s"}`, id, amount, ref)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Payment not found","status":404}`)
	})
	mux.HandleFunc("POST /v1/payments/{id}/refunds", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.refundKeys = append(f.refundKeys, r.Header.Get("X-Idempotency-Key"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":1,"status":"approved"}`)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func mpID(paymentID string) string {
	return "9" + strings.NewReplacer("-", "", "a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6").Replace(paymentID)[:12]
}

// Mercado Pago (adapter real contra uma API fake): preferência → webhook assinado → GET do
// pagamento → PaymentApproved; webhook com assinatura inválida → 401; estorno com idempotency key.
func TestSaga_MercadoPagoComWebhookAssinado(t *testing.T) {
	mp := newFakeMercadoPago(t)
	s := startService(t, config.GatewayMercadoPago, func(c *config.Config) {
		c.MPBaseURL = mp.server.URL
		c.MPAccessToken = "APP_USR-component"
		c.MPWebhookSecret = webhookSecret
		c.MPNotificationURL = "https://api.example.com/billing/api/v1/webhooks/mercadopago"
		c.MPWebhookMaxAge = 5 * time.Minute
	})
	osID := "os-" + uuid.NewString()
	client := token(t, "client", ownerDocument)

	s.send(t, contracts.CmdCreateBudget, osID, createBudget(osID, ""))
	b := decode[contracts.BudgetCreated](t, s.events.await(t, contracts.EvtBudgetCreated, osID))
	s.http(t, "POST", "/api/v1/budgets/"+b.BudgetID+"/approve", client, "")
	s.events.await(t, contracts.EvtBudgetApproved, osID)

	s.send(t, contracts.CmdRequestPayment, osID, contracts.RequestPayment{BudgetID: b.BudgetID})
	link := decode[contracts.PaymentLinkCreated](t, s.events.await(t, contracts.EvtPaymentLinkCreated, osID))
	if !strings.HasPrefix(link.CheckoutURL, "https://www.mercadopago.com.br/checkout/") {
		t.Fatalf("checkout deveria ser o init_point: %s", link.CheckoutURL)
	}
	if status, _ := s.http(t, "POST", "/api/v1/payments/"+link.PaymentID+"/simulate", token(t, "admin", ""), `{"status":"approved"}`); status != 403 {
		t.Fatalf("simulate precisa estar desligado com o Mercado Pago: %d", status)
	}

	providerID := mpID(link.PaymentID)
	webhook := func(signature string) int {
		req, _ := http.NewRequest("POST", s.baseURL+"/api/v1/webhooks/mercadopago?data.id="+providerID+"&type=payment",
			strings.NewReader(`{"action":"payment.updated","type":"payment","data":{"id":"`+providerID+`"}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-request-id", "req-"+providerID)
		req.Header.Set("x-signature", signature)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	ts := fmt.Sprint(time.Now().UnixMilli())
	if code := webhook(mercadopago.Sign("segredo-errado", providerID, "req-"+providerID, ts)); code != 401 {
		t.Fatalf("webhook com assinatura inválida: %d", code)
	}
	if code := webhook(mercadopago.Sign(webhookSecret, providerID, "req-"+providerID, ts)); code != 200 {
		t.Fatalf("webhook assinado: %d", code)
	}
	approved := decode[contracts.PaymentApproved](t, s.events.await(t, contracts.EvtPaymentApproved, osID))
	if approved.ProviderPaymentID != providerID || approved.Amount != 180.5 {
		t.Fatalf("PaymentApproved: %+v", approved)
	}
	// Reentrega do mesmo webhook: idempotente (200, sem novo efeito).
	if code := webhook(mercadopago.Sign(webhookSecret, providerID, "req-"+providerID, ts)); code != 200 {
		t.Fatalf("reentrega do webhook: %d", code)
	}

	s.send(t, contracts.CmdRefundPayment, osID, contracts.RefundPayment{BudgetID: b.BudgetID, Reason: "compensação"})
	s.events.await(t, contracts.EvtPaymentRefunded, osID)
	mp.mu.Lock()
	keys := append([]string(nil), mp.refundKeys...)
	mp.mu.Unlock()
	if len(keys) != 1 || keys[0] != link.PaymentID {
		t.Fatalf("estorno deveria usar o id do pagamento como idempotency key: %v", keys)
	}
}
