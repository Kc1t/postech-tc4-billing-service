// Package paymenthandler expõe os pagamentos na API: consulta (admin ou cliente dono), o
// simulador de retorno do provedor e a página de checkout do gateway fake.
package paymenthandler

import (
	"html/template"
	"net/http"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/presenter"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/gin-gonic/gin"
)

// Handler agrupa as rotas de pagamento.
type Handler struct {
	get      ports.GetPaymentUseCase
	simulate ports.SimulatePaymentUseCase
}

func NewHandler(get ports.GetPaymentUseCase, simulate ports.SimulatePaymentUseCase) *Handler {
	return &Handler{get: get, simulate: simulate}
}

// SimulateRequest é o retorno simulado do provedor.
type SimulateRequest struct {
	Status string `json:"status" binding:"required,oneof=approved rejected" example:"approved" enums:"approved,rejected"`
}

// Get godoc
// @Summary     Consultar pagamento
// @Description Status da cobrança e checkout_url. Admin vê todos; cliente só os do próprio CPF.
// @Tags        payments
// @Produce     json
// @Param       id  path     string true "ID do pagamento (UUID)"
// @Success     200 {object} presenter.PaymentResponse
// @Failure     401 {object} httputil.ErrorResponse
// @Failure     403 {object} httputil.ErrorResponse
// @Failure     404 {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /payments/{id} [get]
func (h *Handler) Get(c *gin.Context) {
	p, err := h.get.Execute(c.Request.Context(), c.Param("id"), httputil.ActorFrom(c))
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.Payment(p))
}

// Simulate godoc
// @Summary     Simular retorno do provedor (gateway fake)
// @Description Só admin e só com PAYMENT_GATEWAY=fake. Aplica o resultado pelas mesmas regras do webhook real: approved publica billing.PaymentApproved; rejected publica billing.PaymentFailed.
// @Tags        payments
// @Accept      json
// @Produce     json
// @Param       id   path     string          true "ID do pagamento (UUID)"
// @Param       body body     SimulateRequest true "Resultado simulado"
// @Success     200  {object} presenter.PaymentResponse
// @Failure     400  {object} httputil.ErrorResponse
// @Failure     401  {object} httputil.ErrorResponse
// @Failure     403  {object} httputil.ErrorResponse
// @Failure     404  {object} httputil.ErrorResponse
// @Failure     409  {object} httputil.ErrorResponse
// @Security    BearerAuth
// @Router      /payments/{id}/simulate [post]
func (h *Handler) Simulate(c *gin.Context) {
	var req SimulateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, err)
		return
	}
	p, err := h.simulate.Execute(c.Request.Context(), ports.SimulatePaymentInput{
		PaymentID: c.Param("id"), Status: req.Status, CorrelationID: httputil.CorrelationID(c),
	})
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	c.JSON(http.StatusOK, presenter.Payment(p))
}

var fakeCheckoutPage = template.Must(template.New("checkout").Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Checkout simulado</title>
<style>body{font-family:system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem;line-height:1.5}code{background:#eee;padding:.1rem .3rem}</style>
</head><body>
<h1>Checkout simulado</h1>
<p>Este link foi gerado pelo gateway <strong>fake</strong> do billing-service (PAYMENT_GATEWAY=fake).</p>
<p>Pagamento <code>{{.}}</code></p>
<p>Para simular o retorno do provedor, chame com um token de admin:</p>
<pre><code>POST /api/v1/payments/{{.}}/simulate
{"status": "approved"}   ou   {"status": "rejected"}</code></pre>
</body></html>`))

// FakeCheckout é a página para onde aponta o checkout_url do gateway fake.
func (h *Handler) FakeCheckout(c *gin.Context) {
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = fakeCheckoutPage.Execute(c.Writer, c.Param("payment_id"))
}
