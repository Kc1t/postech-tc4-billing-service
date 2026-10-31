package mercadopago

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/httpclient"
)

const token = "APP_USR-test-token"

// fakeMP simula a API do Mercado Pago: cada teste registra os handlers das rotas que usa.
func fakeMP(t *testing.T, routes map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid access token","error":"unauthorized","status":401}`))
			return
		}
		h, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"resource not found","error":"not_found","status":404}`))
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newGateway(t *testing.T, baseURL string, cfg ...Config) *Gateway {
	t.Helper()
	c := Config{BaseURL: baseURL + "/", AccessToken: token, NotificationURL: "https://api.example.com/billing/api/v1/webhooks/mercadopago", PreferenceTTL: 48 * time.Hour}
	if len(cfg) > 0 {
		c = cfg[0]
	}
	client := httpclient.New(httpclient.Config{Name: "mercadopago", Timeout: time.Second, MaxRetries: 2, BaseBackoff: time.Millisecond, FailureThreshold: 3, OpenTimeout: time.Minute}, nil)
	g := New(c, client)
	g.now = func() time.Time { return time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC) }
	return g
}

func checkoutReq() ports.CheckoutRequest {
	return ports.CheckoutRequest{PaymentID: "pay-123", BudgetID: "bud-1", OSCode: 42, Amount: 450.004}
}

func TestCreateCheckout(t *testing.T) {
	var got preferenceRequest
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"POST /checkout/preferences": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("content-type: %q", r.Header.Get("Content-Type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"202809963-920c288b","init_point":"https://www.mercadopago.com.br/checkout/v1/redirect?pref_id=202809963-920c288b","sandbox_init_point":"https://sandbox.mercadopago.com.br/x"}`))
		},
	})
	g := newGateway(t, srv.URL)

	prefID, url, err := g.CreateCheckout(context.Background(), checkoutReq())
	if err != nil {
		t.Fatal(err)
	}
	if prefID != "202809963-920c288b" || !strings.Contains(url, "www.mercadopago.com.br") {
		t.Fatalf("deveria usar init_point (nunca sandbox_init_point): %s %s", prefID, url)
	}
	if got.ExternalReference != "pay-123" || got.NotificationURL == "" || !got.Expires || got.BinaryMode {
		t.Fatalf("preferência: %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].UnitPrice != 450 || got.Items[0].CurrencyID != "BRL" || got.Items[0].Quantity != 1 ||
		got.Items[0].ID != "bud-1" || !strings.Contains(got.Items[0].Title, "#42") {
		t.Fatalf("item: %+v", got.Items)
	}
	if got.ExpirationDateFrom != "2026-10-31T13:00:00.000+00:00" || got.ExpirationDateTo != "2026-11-02T13:00:00.000+00:00" {
		t.Fatalf("vigência: %s → %s", got.ExpirationDateFrom, got.ExpirationDateTo)
	}
	if g.Provider() != entities.ProviderMercadoPago {
		t.Fatal("provider")
	}
}

func TestCreateCheckout_Erros(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   error
		calls  int32
	}{
		"validação (definitiva)":    {http.StatusBadRequest, `{"message":"unit_price invalid","error":"bad_request"}`, ports.ErrGatewayRejected, 1},
		"credencial (configuração)": {http.StatusForbidden, `{"error":"forbidden"}`, ports.ErrGatewayUnavailable, 1},
		"5xx não repete POST":       {http.StatusBadGateway, ``, ports.ErrGatewayUnavailable, 1},
		"429":                       {http.StatusTooManyRequests, ``, ports.ErrGatewayUnavailable, 1},
		"resposta sem init_point":   {http.StatusCreated, `{"id":"x"}`, ports.ErrGatewayUnavailable, 1},
		"resposta malformada":       {http.StatusCreated, `{"id":`, ports.ErrGatewayUnavailable, 1},
		"redirecionamento":          {http.StatusNotModified, ``, ports.ErrGatewayUnavailable, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			srv := fakeMP(t, map[string]http.HandlerFunc{
				"POST /checkout/preferences": func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				},
			})
			_, _, err := newGateway(t, srv.URL).CreateCheckout(context.Background(), checkoutReq())
			if !errors.Is(err, tc.want) {
				t.Fatalf("esperava %v, veio %v", tc.want, err)
			}
			if calls.Load() != tc.calls {
				t.Fatalf("chamadas = %d, esperava %d", calls.Load(), tc.calls)
			}
		})
	}

	t.Run("mensagem do provedor no erro", func(t *testing.T) {
		srv := fakeMP(t, map[string]http.HandlerFunc{
			"POST /checkout/preferences": func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_items"}`))
			},
		})
		_, _, err := newGateway(t, srv.URL).CreateCheckout(context.Background(), checkoutReq())
		if err == nil || !strings.Contains(err.Error(), "invalid_items") || strings.Contains(err.Error(), token) {
			t.Fatalf("erro deveria trazer o motivo e nunca o token: %v", err)
		}
	})

	t.Run("token inválido", func(t *testing.T) {
		srv := fakeMP(t, nil)
		g := newGateway(t, srv.URL, Config{BaseURL: srv.URL, AccessToken: "errado"})
		if _, _, err := g.CreateCheckout(context.Background(), checkoutReq()); !errors.Is(err, ports.ErrGatewayUnavailable) {
			t.Fatalf("veio %v", err)
		}
	})

	t.Run("rede fora e contexto cancelado", func(t *testing.T) {
		srv := fakeMP(t, nil)
		g := newGateway(t, srv.URL)
		srv.Close()
		if _, _, err := g.CreateCheckout(context.Background(), checkoutReq()); !errors.Is(err, ports.ErrGatewayUnavailable) {
			t.Fatalf("veio %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := g.CreateCheckout(ctx, checkoutReq()); errors.Is(err, ports.ErrGatewayUnavailable) || err == nil {
			t.Fatalf("cancelamento não é indisponibilidade do provedor: %v", err)
		}
	})
}

func TestGetPayment(t *testing.T) {
	var calls atomic.Int32
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"GET /v1/payments/123456": func(w http.ResponseWriter, _ *http.Request) {
			// Primeira tentativa falha com 503: GET é idempotente e o httpclient repete.
			if calls.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"id":123456,"status":"approved","status_detail":"accredited","transaction_amount":450.0,"external_reference":"pay-123"}`))
		},
		"GET /v1/payments/abc": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"id":"abc","status":"rejected","status_detail":"cc_rejected_other_reason","transaction_amount":10,"external_reference":"pay-9"}`))
		},
	})
	g := newGateway(t, srv.URL)

	got, err := g.GetPayment(context.Background(), "123456")
	if err != nil {
		t.Fatal(err)
	}
	want := ports.ProviderPayment{ID: "123456", Status: ports.ProviderStatusApproved, StatusDetail: "accredited", Amount: 450, ExternalReference: "pay-123"}
	if got != want || calls.Load() != 2 {
		t.Fatalf("got %+v (chamadas %d)", got, calls.Load())
	}
	if got, err := g.GetPayment(context.Background(), "abc"); err != nil || got.ID != "abc" || got.Status != ports.ProviderStatusRejected {
		t.Fatalf("id string: %+v %v", got, err)
	}
	if _, err := g.GetPayment(context.Background(), "999"); !errors.Is(err, ports.ErrGatewayNotFound) {
		t.Fatalf("404: veio %v", err)
	}
}

func TestGetPayment_IDInvalido(t *testing.T) {
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"GET /v1/payments/1": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"id":{"x":1}}`)) },
		"GET /v1/payments/2": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"id":"\x"}`)) },
	})
	g := newGateway(t, srv.URL)
	for _, id := range []string{"1", "2"} {
		if _, err := g.GetPayment(context.Background(), id); !errors.Is(err, ports.ErrGatewayUnavailable) {
			t.Fatalf("id %s: veio %v", id, err)
		}
	}
}

func TestRefund(t *testing.T) {
	var key, body string
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"POST /v1/payments/123/refunds": func(w http.ResponseWriter, r *http.Request) {
			key = r.Header.Get("X-Idempotency-Key")
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1009042015,"payment_id":123,"amount":450,"status":"approved"}`))
		},
		"POST /v1/payments/400/refunds": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Payment too old to be refunded"}`))
		},
	})
	g := newGateway(t, srv.URL)

	if err := g.Refund(context.Background(), "123", "pay-123"); err != nil {
		t.Fatal(err)
	}
	if key != "pay-123" || body != "{}" {
		t.Fatalf("idempotency key %q, corpo %q (vazio = estorno total)", key, body)
	}
	if err := g.Refund(context.Background(), "400", "k"); !errors.Is(err, ports.ErrGatewayRejected) {
		t.Fatalf("veio %v", err)
	}
	if err := g.Refund(context.Background(), "123", ""); !errors.Is(err, ports.ErrGatewayRejected) {
		t.Fatalf("sem chave de idempotência: veio %v", err)
	}
}

func TestCancel(t *testing.T) {
	var got map[string]string
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"PUT /v1/payments/77": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"id":77,"status":"cancelled"}`))
		},
		"PUT /v1/payments/78": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Invalid status for cancellation"}`))
		},
	})
	g := newGateway(t, srv.URL)
	if err := g.Cancel(context.Background(), "77"); err != nil || got["status"] != "cancelled" {
		t.Fatalf("err=%v corpo=%v", err, got)
	}
	if err := g.Cancel(context.Background(), "78"); !errors.Is(err, ports.ErrGatewayRejected) {
		t.Fatalf("veio %v", err)
	}
}

func TestCircuitBreakerAbreComProvedorFora(t *testing.T) {
	var calls atomic.Int32
	srv := fakeMP(t, map[string]http.HandlerFunc{
		"GET /v1/payments/1": func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	g := newGateway(t, srv.URL)
	_, _ = g.GetPayment(context.Background(), "1") // 3 tentativas = limiar do circuito
	before := calls.Load()
	_, err := g.GetPayment(context.Background(), "1")
	if !errors.Is(err, ports.ErrGatewayUnavailable) || !errors.Is(err, httpclient.ErrCircuitOpen) {
		t.Fatalf("esperava circuito aberto, veio %v", err)
	}
	if calls.Load() != before {
		t.Fatal("com o circuito aberto a chamada não pode chegar ao provedor")
	}
}

func TestNewDefaults(t *testing.T) {
	g := New(Config{}, httpclient.New(httpclient.Config{Name: "mp"}, nil))
	if g.cfg.BaseURL != DefaultBaseURL || g.cfg.PreferenceTTL != 24*time.Hour {
		t.Fatalf("defaults: %+v", g.cfg)
	}
}
