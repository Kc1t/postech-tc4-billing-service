// Package webhookhandler recebe as notificações do Mercado Pago (rota pública, autenticada pela
// assinatura x-signature em vez de JWT).
package webhookhandler

import (
	"encoding/json"
	"io"
	"net/http"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	"github.com/gin-gonic/gin"
)

// maxBody limita o corpo lido (a notificação real tem algumas centenas de bytes).
const maxBody = 64 << 10

// Handler trata POST /webhooks/mercadopago.
type Handler struct {
	verifier ports.WebhookVerifier
	process  ports.ProcessPaymentNotificationUseCase
}

func NewHandler(verifier ports.WebhookVerifier, process ports.ProcessPaymentNotificationUseCase) *Handler {
	return &Handler{verifier: verifier, process: process}
}

// AckResponse confirma o recebimento.
type AckResponse struct {
	Status string `json:"status" example:"processed" enums:"processed,ignored"`
}

type notificationBody struct {
	Type   string `json:"type"`
	Action string `json:"action"`
}

// MercadoPago godoc
// @Summary     Webhook do Mercado Pago
// @Description Rota pública. Valida x-signature (HMAC-SHA256 do manifest id:{data.id};request-id:{x-request-id};ts:{ts}; com MP_WEBHOOK_SECRET) e relê o pagamento em GET /v1/payments/{data.id}. Notificações que não são de pagamento (ou sem data.id na query) são confirmadas e ignoradas. 5xx faz o Mercado Pago reentregar.
// @Tags        webhooks
// @Accept      json
// @Produce     json
// @Param       data.id      query    string true  "ID do pagamento no Mercado Pago"
// @Param       type         query    string false "Tipo do recurso (payment)"
// @Param       x-signature  header   string true  "ts=...,v1=..."
// @Param       x-request-id header   string true  "ID da notificação"
// @Success     200 {object} AckResponse
// @Failure     401 {object} httputil.ErrorResponse
// @Failure     503 {object} httputil.ErrorResponse
// @Router      /api/v1/webhooks/mercadopago [post]
func (h *Handler) MercadoPago(c *gin.Context) {
	log := logger.FromContext(c.Request.Context())
	dataID := c.Query("data.id")
	typ := c.Query("type")

	var body notificationBody
	if raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody)); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if typ == "" {
		typ = body.Type
	}

	// O manifest assinado usa o data.id da query string; sem ele (ou fora do tipo "payment",
	// como o IPN legado ?topic=...&id=...) não há o que processar.
	if dataID == "" || typ != "payment" {
		log.Info("webhook_ignored", "type", typ, "action", body.Action, "has_data_id", dataID != "")
		c.JSON(http.StatusOK, AckResponse{Status: "ignored"})
		return
	}

	if !h.verifier.Verify(c.GetHeader("x-signature"), c.GetHeader("x-request-id"), dataID) {
		log.Warn("webhook_signature_invalid", "provider_payment_id", dataID)
		httputil.Abort(c, http.StatusUnauthorized, httputil.CodeInvalidSignature, "assinatura do webhook inválida")
		return
	}

	err := h.process.Execute(c.Request.Context(), ports.PaymentNotificationInput{
		ProviderPaymentID: dataID, CorrelationID: httputil.CorrelationID(c),
	})
	if err != nil {
		httputil.WriteError(c, err)
		return
	}
	log.Info("webhook_processed", "provider_payment_id", dataID, "action", body.Action)
	c.JSON(http.StatusOK, AckResponse{Status: "processed"})
}
