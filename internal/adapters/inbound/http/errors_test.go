package httputil

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestWriteError_Tabela(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domainerrors.ErrBudgetNotFound, 404, CodeBudgetNotFound},
		{domainerrors.ErrPaymentNotFound, 404, CodePaymentNotFound},
		{domainerrors.ErrNotOwner, 403, CodeForbidden},
		{domainerrors.ErrSimulationDisabled, 403, CodeSimulationDisabled},
		{fmt.Errorf("%w: orçamento approved → approved", domainerrors.ErrInvalidTransition), 409, CodeInvalidTransition},
		{domainerrors.ErrBudgetNotApproved, 409, CodeBudgetNotApproved},
		{domainerrors.ErrConcurrentUpdate, 409, CodeConcurrentUpdate},
		{domainerrors.ErrInvalidInput, 400, CodeInvalidRequest},
		{fmt.Errorf("%w: timeout", ports.ErrGatewayUnavailable), 503, CodeGatewayUnavailable},
		{errors.New("qualquer outra coisa"), 500, CodeInternal},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		WriteError(c, tc.err)

		var resp ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%v: corpo fora do padrão %s", tc.err, w.Body.String())
		}
		if w.Code != tc.status || resp.Error.Code != tc.code || resp.Error.Message == "" {
			t.Errorf("%v: status %d código %s", tc.err, w.Code, resp.Error.Code)
		}
		if len(c.Errors) != 1 {
			t.Errorf("%v: erro deveria ficar registrado no contexto para o log", tc.err)
		}
	}
}

func TestBadRequestEContexto(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	BadRequest(c, errors.New("campo x obrigatório"))
	if w.Code != 400 {
		t.Fatalf("status %d", w.Code)
	}

	c.Set(KeyCorrelationID, "corr")
	c.Set(KeyUserID, "u")
	c.Set(KeyUserRole, "client")
	c.Set(KeyUserDocument, "52998224725")
	if CorrelationID(c) != "corr" {
		t.Fatal("correlation id")
	}
	if a := ActorFrom(c); a != (entities.Actor{UserID: "u", Role: entities.RoleClient, Document: "52998224725"}) {
		t.Fatalf("ator: %+v", a)
	}
}
