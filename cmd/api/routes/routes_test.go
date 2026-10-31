package routes

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	budgethandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/budget"
	healthhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/health"
	paymenthandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/payment"
	webhookhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/webhook"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/mercadopago"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports/mocks"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/newrelic/go-agent/v3/newrelic"
	"go.uber.org/mock/gomock"
)

func init() { gin.SetMode(gin.TestMode) }

const secret = "segredo"

type fixture struct {
	router   *gin.Engine
	getByID  *mocks.MockGetBudgetByIDUseCase
	getByOS  *mocks.MockGetBudgetByOSUseCase
	approve  *mocks.MockApproveBudgetUseCase
	reject   *mocks.MockRejectBudgetUseCase
	getPay   *mocks.MockGetPaymentUseCase
	simulate *mocks.MockSimulatePaymentUseCase
}

func setup(t *testing.T, fake bool, nr *newrelic.Application) fixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := fixture{
		getByID:  mocks.NewMockGetBudgetByIDUseCase(ctrl),
		getByOS:  mocks.NewMockGetBudgetByOSUseCase(ctrl),
		approve:  mocks.NewMockApproveBudgetUseCase(ctrl),
		reject:   mocks.NewMockRejectBudgetUseCase(ctrl),
		getPay:   mocks.NewMockGetPaymentUseCase(ctrl),
		simulate: mocks.NewMockSimulatePaymentUseCase(ctrl),
	}
	f.router = New(Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewRelic:       nr,
		JWTSecret:      secret,
		AllowedOrigins: []string{"http://localhost:3000"},
		FakeCheckout:   fake,
		Health:         healthhandler.NewHandler(map[string]healthhandler.Check{"database": func(context.Context) error { return nil }}, time.Second),
		Budget:         budgethandler.NewHandler(f.getByID, f.getByOS, f.approve, f.reject),
		Payment:        paymenthandler.NewHandler(f.getPay, f.simulate),
		Webhook:        webhookhandler.NewHandler(mercadopago.NewSignatureVerifier("", 0), mocks.NewMockProcessPaymentNotificationUseCase(ctrl)),
	})
	return f
}

func bearer(t *testing.T, role string) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": "u", "role": role, "exp": time.Now().Add(time.Hour).Unix()}
	if role == "client" {
		claims["document"] = "52998224725"
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return "Bearer " + s
}

func call(r http.Handler, method, path, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPapeisPorRota(t *testing.T) {
	f := setup(t, true, nil)
	f.getByID.EXPECT().Execute(gomock.Any(), "b1", gomock.Any()).Return(ports.BudgetView{}, domainerrors.ErrBudgetNotFound).Times(2)
	f.getByOS.EXPECT().Execute(gomock.Any(), "os1", gomock.Any()).Return(ports.BudgetView{}, domainerrors.ErrBudgetNotFound).Times(2)
	f.getPay.EXPECT().Execute(gomock.Any(), "p1", gomock.Any()).Return(nil, domainerrors.ErrPaymentNotFound).Times(2)
	f.approve.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrBudgetNotFound)
	f.reject.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrBudgetNotFound)
	f.simulate.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrPaymentNotFound)

	admin, client, mechanic := bearer(t, "admin"), bearer(t, "client"), bearer(t, "mechanic")
	cases := []struct {
		method, path, auth, body string
		want                     int
	}{
		// Sem token: 401 em todas as rotas protegidas.
		{"GET", "/api/v1/budgets/b1", "", "", 401},
		{"POST", "/api/v1/budgets/b1/approve", "", "", 401},
		{"GET", "/api/v1/payments/p1", "", "", 401},
		// Leitura: admin e cliente chegam ao caso de uso (404 aqui); mecânico não consulta cobrança.
		{"GET", "/api/v1/budgets/b1", admin, "", 404},
		{"GET", "/api/v1/budgets/b1", client, "", 404},
		{"GET", "/api/v1/budgets/b1", mechanic, "", 403},
		{"GET", "/api/v1/budgets/os/os1", admin, "", 404},
		{"GET", "/api/v1/budgets/os/os1", client, "", 404},
		{"GET", "/api/v1/payments/p1", admin, "", 404},
		{"GET", "/api/v1/payments/p1", client, "", 404},
		// Decisão: só cliente.
		{"POST", "/api/v1/budgets/b1/approve", admin, "", 403},
		{"POST", "/api/v1/budgets/b1/approve", client, "", 404},
		{"POST", "/api/v1/budgets/b1/reject", client, `{"reason":"caro"}`, 404},
		// Simulação: só admin.
		{"POST", "/api/v1/payments/p1/simulate", client, `{"status":"approved"}`, 403},
		{"POST", "/api/v1/payments/p1/simulate", admin, `{"status":"approved"}`, 404},
		// Webhook é público; sem assinatura válida → 401 (o gateway fake não tem segredo: falha fechada).
		{"POST", "/api/v1/webhooks/mercadopago?data.id=1&type=payment", "", `{}`, 401},
		// Públicas.
		{"GET", "/health/live", "", "", 200},
		{"GET", "/health/ready", "", "", 200},
		{"GET", "/swagger/index.html", "", "", 200},
		{"GET", "/fake-checkout/p1", "", "", 200},
	}
	for _, tc := range cases {
		w := call(f.router, tc.method, tc.path, tc.auth, tc.body)
		if w.Code != tc.want {
			t.Errorf("%s %s (%s): status %d, esperado %d — %s", tc.method, tc.path, roleOf(tc.auth, admin, client, mechanic), w.Code, tc.want, w.Body.String())
		}
		if w.Header().Get("X-Correlation-ID") == "" {
			t.Errorf("%s %s: resposta sem X-Correlation-ID", tc.method, tc.path)
		}
	}
}

func roleOf(auth, admin, client, mechanic string) string {
	switch auth {
	case admin:
		return "admin"
	case client:
		return "client"
	case mechanic:
		return "mechanic"
	}
	return "anônimo"
}

func TestSwaggerDoc(t *testing.T) {
	f := setup(t, false, nil)
	w := call(f.router, "GET", "/swagger/doc.json", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/api/v1/budgets/{id}/approve") {
		t.Fatalf("especificação gerada não registrada: %d", w.Code)
	}
}

func TestFakeCheckoutSoComGatewayFake(t *testing.T) {
	f := setup(t, false, nil)
	w := call(f.router, "GET", "/fake-checkout/p1", "", "")
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"NOT_FOUND"`) {
		t.Fatalf("com o Mercado Pago a página fake não existe: %d %s", w.Code, w.Body.String())
	}
}

func TestComNewRelic(t *testing.T) {
	app, err := newrelic.NewApplication(newrelic.ConfigAppName("billing-test"), newrelic.ConfigEnabled(false))
	if err != nil {
		t.Fatal(err)
	}
	f := setup(t, false, app)
	if w := call(f.router, "GET", "/health/live", "", ""); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}
