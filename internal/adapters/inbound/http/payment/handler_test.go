package paymenthandler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/presenter"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports/mocks"
	"github.com/gin-gonic/gin"
	"go.uber.org/mock/gomock"
)

func init() { gin.SetMode(gin.TestMode) }

func newRouter(t *testing.T, actor entities.Actor) (*gin.Engine, *mocks.MockGetPaymentUseCase, *mocks.MockSimulatePaymentUseCase) {
	t.Helper()
	ctrl := gomock.NewController(t)
	get := mocks.NewMockGetPaymentUseCase(ctrl)
	sim := mocks.NewMockSimulatePaymentUseCase(ctrl)
	h := NewHandler(get, sim)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(httputil.KeyCorrelationID, "corr-1")
		c.Set(httputil.KeyUserRole, string(actor.Role))
		c.Set(httputil.KeyUserDocument, actor.Document)
	})
	r.GET("/payments/:id", h.Get)
	r.POST("/payments/:id/simulate", h.Simulate)
	r.GET("/fake-checkout/:payment_id", h.FakeCheckout)
	return r, get, sim
}

func payment(t *testing.T) *entities.Payment {
	t.Helper()
	b, _ := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 5, SagaID: "s-1", Requester: entities.Requester{Document: "52998224725"},
		Items: []entities.BudgetItem{{Kind: entities.ItemService, Ref: "1", Quantity: 1, UnitPrice: 80}}, Total: 80,
	}, time.Now())
	_ = b.Approve(time.Now())
	p, err := entities.NewPayment(b, entities.ProviderFake, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = p.LinkCreated("pref", "http://localhost:8082/fake-checkout/"+p.ID(), time.Now())
	return p
}

func TestGet(t *testing.T) {
	actor := entities.Actor{Role: entities.RoleClient, Document: "52998224725"}
	r, get, _ := newRouter(t, actor)
	p := payment(t)
	get.EXPECT().Execute(gomock.Any(), p.ID(), actor).Return(p, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/payments/"+p.ID(), nil))
	var got presenter.PaymentResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.ID != p.ID() || got.Status != "pending" || got.CheckoutURL == "" || got.Amount != 80 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	get.EXPECT().Execute(gomock.Any(), "x", actor).Return(nil, domainerrors.ErrPaymentNotFound)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/payments/x", nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "PAYMENT_NOT_FOUND") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestSimulate(t *testing.T) {
	r, _, sim := newRouter(t, entities.Actor{Role: entities.RoleAdmin})
	p := payment(t)
	sim.EXPECT().Execute(gomock.Any(), ports.SimulatePaymentInput{PaymentID: p.ID(), Status: "approved", CorrelationID: "corr-1"}).Return(p, nil)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/payments/"+p.ID()+"/simulate", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		return w
	}
	if w := post(`{"status":"approved"}`); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"status":"pending"}`} {
		if w := post(body); w.Code != 400 || !strings.Contains(w.Body.String(), "INVALID_REQUEST") {
			t.Fatalf("%s: status %d", body, w.Code)
		}
	}
	sim.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrSimulationDisabled)
	if w := post(`{"status":"rejected"}`); w.Code != 403 || !strings.Contains(w.Body.String(), "SIMULATION_DISABLED") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestFakeCheckout_EscapaOParametro(t *testing.T) {
	r, _, _ := newRouter(t, entities.Actor{})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/fake-checkout/%3Cscript%3E", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "<script>") || !strings.Contains(w.Body.String(), "&lt;script&gt;") {
		t.Fatal("o id precisa ser escapado no HTML")
	}
}
