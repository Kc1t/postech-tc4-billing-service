// Package budgethandler expõe os orçamentos na API: consulta (admin ou cliente dono) e a decisão
// do cliente (aprovar/recusar). A posse é verificada nos casos de uso.
package budgethandler

import (
	"net/http"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/presenter"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/gin-gonic/gin"
)

// Handler agrupa as rotas de orçamento.
type Handler struct {
	getByID ports.GetBudgetByIDUseCase
	getByOS ports.GetBudgetByOSUseCase
	approve ports.ApproveBudgetUseCase
	reject  ports.RejectBudgetUseCase
}

func NewHandler(getByID ports.GetBudgetByIDUseCase, getByOS ports.GetBudgetByOSUseCase,
	approve ports.ApproveBudgetUseCase, reject ports.RejectBudgetUseCase) *Handler {
	return &Handler{getByID: getByID, getByOS: getByOS, approve: approve, reject: reject}
}

// RejectRequest é o corpo da recusa.
type RejectRequest struct {
	Reason string `json:"reason" binding:"required,max=500" example:"Vou fazer o reparo em outro momento"`
}

// GetByID godoc
// @Summary     Consultar orçamento por ID
// @Description Orçamento com itens, total, status e a cobrança mais recente. Admin vê todos; cliente só os do próprio CPF.
// @Tags        budgets
// @Produce     json
// @Param       id  path     string true "ID do orçamento (UUID)"
// @Success     200 {object} presenter.BudgetResponse
// @Failure     401 {object} httputil.ErrorResponse
// @Failure     403 {object} httputil.ErrorResponse
// @Failure     404 {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /budgets/{id} [get]
func (h *Handler) GetByID(c *gin.Context) {
	v, err := h.getByID.Execute(c.Request.Context(), c.Param("id"), httputil.ActorFrom(c))
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.View(v))
}

// GetByOS godoc
// @Summary     Consultar orçamento da ordem de serviço
// @Description Usado pela API composition do os-service (GET /service-orders/{id}/details).
// @Tags        budgets
// @Produce     json
// @Param       os_id path     string true "ID da OS"
// @Success     200   {object} presenter.BudgetResponse
// @Failure     401   {object} httputil.ErrorResponse
// @Failure     403   {object} httputil.ErrorResponse
// @Failure     404   {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /budgets/os/{os_id} [get]
func (h *Handler) GetByOS(c *gin.Context) {
	v, err := h.getByOS.Execute(c.Request.Context(), c.Param("os_id"), httputil.ActorFrom(c))
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.View(v))
}

// Approve godoc
// @Summary     Aprovar orçamento
// @Description Só o cliente dono (token emitido pelo CPF) e só em pending_approval. Publica billing.BudgetApproved.
// @Tags        budgets
// @Produce     json
// @Param       id  path     string true "ID do orçamento (UUID)"
// @Success     200 {object} presenter.BudgetResponse
// @Failure     401 {object} httputil.ErrorResponse
// @Failure     403 {object} httputil.ErrorResponse
// @Failure     404 {object} httputil.ErrorResponse
// @Failure     409 {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /budgets/{id}/approve [post]
func (h *Handler) Approve(c *gin.Context) {
	b, err := h.approve.Execute(c.Request.Context(), ports.DecideBudgetInput{
		BudgetID: c.Param("id"), Actor: httputil.ActorFrom(c), CorrelationID: httputil.CorrelationID(c),
	})
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.Budget(b, nil))
}

// Reject godoc
// @Summary     Recusar orçamento
// @Description Só o cliente dono e só em pending_approval; motivo obrigatório. Publica billing.BudgetRejected.
// @Tags        budgets
// @Accept      json
// @Produce     json
// @Param       id   path     string        true "ID do orçamento (UUID)"
// @Param       body body     RejectRequest true "Motivo da recusa"
// @Success     200  {object} presenter.BudgetResponse
// @Failure     400  {object} httputil.ErrorResponse
// @Failure     401  {object} httputil.ErrorResponse
// @Failure     403  {object} httputil.ErrorResponse
// @Failure     404  {object} httputil.ErrorResponse
// @Failure     409  {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /budgets/{id}/reject [post]
func (h *Handler) Reject(c *gin.Context) {
	var req RejectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, err)
		return
	}
	b, err := h.reject.Execute(c.Request.Context(), ports.DecideBudgetInput{
		BudgetID: c.Param("id"), Actor: httputil.ActorFrom(c), Reason: req.Reason, CorrelationID: httputil.CorrelationID(c),
	})
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.Budget(b, nil))
}
