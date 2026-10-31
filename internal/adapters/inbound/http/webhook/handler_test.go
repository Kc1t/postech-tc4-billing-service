package webhookhandler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/mercadopago"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports/mocks"
	"github.com/gin-gonic/gin"
	"go.uber.org/mock/gomock"
)

func init() { gin.SetMode(gin.TestMode) }

const (
	secret    = "segredo-do-painel"
	requestID = "bb56a2f1-6aae-46ac-982e-9dcd3581d08e"
	ts        = "1742505638683"
	body      = `{"action":"payment.updated","api_version":"v1","data":{"id":"123456"},"id":"123456","live_mode":false,"type":"payment","user_id":724484980}`
)

// newRouter usa o verificador real do adapter Mercado Pago: o teste cobre a assinatura de ponta a ponta.
func newRouter(t *testing.T) (*gin.Engine, *mocks.MockProcessPaymentNotificationUseCase) {
	t.Helper()
	process := mocks.NewMockProcessPaymentNotificationUseCase(gomock.NewController(t))
	h := NewHandler(mercadopago.NewSignatureVerifier(secret, 0), process)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(httputil.KeyCorrelationID, c.GetHeader("x-request-id")) })
	r.POST("/webhooks/mercadopago", h.MercadoPago)
	return r, process
}

func post(r http.Handler, query, signature, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/mercadopago"+query, bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-request-id", requestID)
	if signature != "" {
		req.Header.Set("x-signature", signature)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebhook_AssinaturaValidaProcessa(t *testing.T) {
	r, process := newRouter(t)
	process.EXPECT().Execute(gomock.Any(), ports.PaymentNotificationInput{ProviderPaymentID: "123456", CorrelationID: requestID}).Return(nil)

	w := post(r, "?data.id=123456&type=payment", mercadopago.Sign(secret, "123456", requestID, ts), body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "processed") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestWebhook_TipoSoNoCorpo(t *testing.T) {
	r, process := newRouter(t)
	process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil)
	if w := post(r, "?data.id=123456", mercadopago.Sign(secret, "123456", requestID, ts), body); w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
}

func TestWebhook_AssinaturaInvalida401(t *testing.T) {
	cases := map[string]struct{ query, sig string }{
		"sem assinatura":        {"?data.id=123456&type=payment", ""},
		"segredo errado":        {"?data.id=123456&type=payment", mercadopago.Sign("outro", "123456", requestID, ts)},
		"data.id trocado":       {"?data.id=999999&type=payment", mercadopago.Sign(secret, "123456", requestID, ts)},
		"assinatura malformada": {"?data.id=123456&type=payment", "v1=abc"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newRouter(t) // o caso de uso não pode ser chamado
			w := post(r, tc.query, tc.sig, body)
			if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "INVALID_SIGNATURE") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWebhook_NotificacoesIgnoradas(t *testing.T) {
	cases := map[string]struct{ query, payload string }{
		"IPN legado (topic/id)":     {"?topic=payment&id=123456", ``},
		"outro tipo de recurso":     {"?data.id=1&type=merchant_order", `{"type":"merchant_order"}`},
		"sem data.id na query":      {"?type=payment", body},
		"corpo inválido e sem tipo": {"?data.id=1", `{`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, _ := newRouter(t)
			w := post(r, tc.query, "", tc.payload)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ignored") {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestWebhook_FalhaTransitoriaPedeReentrega(t *testing.T) {
	r, process := newRouter(t)
	process.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(ports.ErrGatewayUnavailable)
	w := post(r, "?data.id=123456&type=payment", mercadopago.Sign(secret, "123456", requestID, ts), body)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "GATEWAY_UNAVAILABLE") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}
