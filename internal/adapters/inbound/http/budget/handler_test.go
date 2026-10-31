package budgethandler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

const ownerDoc = "52998224725"

type deps struct {
	getByID *mocks.MockGetBudgetByIDUseCase
	getByOS *mocks.MockGetBudgetByOSUseCase
	approve *mocks.MockApproveBudgetUseCase
	reject  *mocks.MockRejectBudgetUseCase
}

// newRouter simula os middlewares Auth e CorrelationID com o ator informado.
func newRouter(t *testing.T, actor entities.Actor) (*gin.Engine, deps) {
	t.Helper()
	ctrl := gomock.NewController(t)
	d := deps{
		getByID: mocks.NewMockGetBudgetByIDUseCase(ctrl),
		getByOS: mocks.NewMockGetBudgetByOSUseCase(ctrl),
		approve: mocks.NewMockApproveBudgetUseCase(ctrl),
		reject:  mocks.NewMockRejectBudgetUseCase(ctrl),
	}
	h := NewHandler(d.getByID, d.getByOS, d.approve, d.reject)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(httputil.KeyCorrelationID, "corr-1")
		c.Set(httputil.KeyUserID, actor.UserID)
		c.Set(httputil.KeyUserRole, string(actor.Role))
		c.Set(httputil.KeyUserDocument, actor.Document)
	})
	// Mesmo desenho de rotas do cmd/api/routes (rota estática "os" convive com o parâmetro :id).
	r.GET("/budgets/os/:os_id", h.GetByOS)
	r.GET("/budgets/:id", h.GetByID)
	r.POST("/budgets/:id/approve", h.Approve)
	r.POST("/budgets/:id/reject", h.Reject)
	return r, d
}

func client() entities.Actor {
	return entities.Actor{UserID: "u1", Role: entities.RoleClient, Document: ownerDoc}
}

func budget(t *testing.T, status entities.BudgetStatus) *entities.Budget {
	t.Helper()
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 5, SagaID: "s-1",
		Requester: entities.Requester{ID: "r1", Name: "Ana", Email: "ana@example.com", Document: ownerDoc},
		Items:     []entities.BudgetItem{{Kind: entities.ItemPart, Ref: "P1", Description: "Peça", Quantity: 2, UnitPrice: 50}},
		Total:     100,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := b.Snapshot()
	s.Status = status
	return entities.RestoreBudget(s)
}

func do(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	r.ServeHTTP(w, req)
	return w
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) httputil.ErrorBody {
	t.Helper()
	var e httputil.ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("erro fora do padrão: %s", w.Body.String())
	}
	return e.Error
}

func TestGetByID(t *testing.T) {
	r, d := newRouter(t, client())
	b := budget(t, entities.BudgetApproved)
	p, _ := entities.NewPayment(b, entities.ProviderFake, time.Now())
	d.getByID.EXPECT().Execute(gomock.Any(), b.ID(), client()).Return(ports.BudgetView{Budget: b, Payment: p}, nil)

	w := do(r, http.MethodGet, "/budgets/"+b.ID(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got presenter.BudgetResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.ID != b.ID() || got.Total != 100 || len(got.Items) != 1 || got.Items[0].Subtotal != 100 || got.Payment == nil || got.Payment.ID != p.ID() {
		t.Fatalf("resposta: %+v", got)
	}
	if bytes.Contains(w.Body.Bytes(), []byte(ownerDoc)) {
		t.Fatal("a resposta não deve expor o CPF")
	}
}

func TestGetByOS(t *testing.T) {
	admin := entities.Actor{UserID: "adm", Role: entities.RoleAdmin}
	r, d := newRouter(t, admin)
	b := budget(t, entities.BudgetPendingApproval)
	d.getByOS.EXPECT().Execute(gomock.Any(), "os-1", admin).Return(ports.BudgetView{Budget: b}, nil)

	w := do(r, http.MethodGet, "/budgets/os/os-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["payment"] != nil || got["status"] != "pending_approval" {
		t.Fatalf("resposta: %v", got)
	}
}

func TestErrosPadronizados(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"inexistente":     {domainerrors.ErrBudgetNotFound, 404, "BUDGET_NOT_FOUND"},
		"não é o dono":    {domainerrors.ErrNotOwner, 403, "FORBIDDEN"},
		"erro inesperado": {errors.New("pq: conexão recusada"), 500, "INTERNAL_ERROR"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r, d := newRouter(t, client())
			d.getByID.EXPECT().Execute(gomock.Any(), "x", gomock.Any()).Return(ports.BudgetView{}, tc.err)
			w := do(r, http.MethodGet, "/budgets/x", "")
			e := decodeErr(t, w)
			if w.Code != tc.status || e.Code != tc.code {
				t.Fatalf("status %d código %s", w.Code, e.Code)
			}
			if tc.status == 500 && bytes.Contains(w.Body.Bytes(), []byte("pq:")) {
				t.Fatal("500 não pode vazar detalhe interno")
			}
		})
	}
	t.Run("os inexistente", func(t *testing.T) {
		r, d := newRouter(t, client())
		d.getByOS.EXPECT().Execute(gomock.Any(), "x", gomock.Any()).Return(ports.BudgetView{}, domainerrors.ErrBudgetNotFound)
		if w := do(r, http.MethodGet, "/budgets/os/x", ""); w.Code != 404 {
			t.Fatalf("status %d", w.Code)
		}
	})
}

func TestApprove(t *testing.T) {
	r, d := newRouter(t, client())
	b := budget(t, entities.BudgetApproved)
	d.approve.EXPECT().Execute(gomock.Any(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: client(), CorrelationID: "corr-1"}).Return(b, nil)

	w := do(r, http.MethodPost, "/budgets/"+b.ID()+"/approve", "")
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"status":"approved"`)) {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	d.approve.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrInvalidTransition)
	w = do(r, http.MethodPost, "/budgets/"+b.ID()+"/approve", "")
	if e := decodeErr(t, w); w.Code != http.StatusConflict || e.Code != "INVALID_TRANSITION" {
		t.Fatalf("status %d código %s", w.Code, e.Code)
	}
}

func TestReject(t *testing.T) {
	r, d := newRouter(t, client())
	b := budget(t, entities.BudgetRejected)
	d.reject.EXPECT().Execute(gomock.Any(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: client(), Reason: "caro", CorrelationID: "corr-1"}).Return(b, nil)

	if w := do(r, http.MethodPost, "/budgets/"+b.ID()+"/reject", `{"reason":"caro"}`); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"reason":""}`, `não é json`} {
		w := do(r, http.MethodPost, "/budgets/"+b.ID()+"/reject", body)
		if e := decodeErr(t, w); w.Code != http.StatusBadRequest || e.Code != "INVALID_REQUEST" {
			t.Fatalf("corpo %q: status %d código %s", body, w.Code, e.Code)
		}
	}
	d.reject.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, domainerrors.ErrConcurrentUpdate)
	w := do(r, http.MethodPost, "/budgets/"+b.ID()+"/reject", `{"reason":"caro"}`)
	if e := decodeErr(t, w); w.Code != http.StatusConflict || e.Code != "CONCURRENT_UPDATE" {
		t.Fatalf("status %d código %s", w.Code, e.Code)
	}
}
