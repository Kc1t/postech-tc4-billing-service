package paymentuc

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// ProcessPaymentNotification trata uma notificação de pagamento já autenticada (assinatura
// validada no adapter do webhook). O corpo da notificação nunca é usado como fonte da verdade:
// o pagamento é sempre relido no provedor (GET /v1/payments/{id}) e o external_reference leva
// ao nosso pagamento.
//
// Devolve erro só para falha transitória (provedor fora, conflito de concorrência): o webhook
// responde 5xx e o Mercado Pago reentrega mais tarde. Notificação de pagamento desconhecido é
// ignorada com sucesso, para não gerar reentregas inúteis.
type ProcessPaymentNotification struct {
	gateway ports.PaymentGateway
	applier *resultApplier
}

func NewProcessPaymentNotification(uow ports.UnitOfWork, gateway ports.PaymentGateway) *ProcessPaymentNotification {
	return &ProcessPaymentNotification{
		gateway: gateway,
		applier: &resultApplier{uow: uow, gateway: gateway, now: time.Now},
	}
}

func (uc *ProcessPaymentNotification) Execute(ctx context.Context, in ports.PaymentNotificationInput) error {
	if in.ProviderPaymentID == "" {
		return fmt.Errorf("%w: id do pagamento ausente na notificação", domainerrors.ErrInvalidInput)
	}
	log := logger.FromContext(ctx).With("provider_payment_id", in.ProviderPaymentID)

	r, err := uc.gateway.GetPayment(ctx, in.ProviderPaymentID)
	if errors.Is(err, ports.ErrGatewayNotFound) {
		log.Warn("notification_ignored", "reason", "pagamento inexistente no provedor")
		return nil
	}
	if err != nil {
		return fmt.Errorf("consultar pagamento %s no provedor: %w", in.ProviderPaymentID, err)
	}
	if r.ID == "" {
		r.ID = in.ProviderPaymentID
	}
	if r.ExternalReference == "" {
		log.Warn("notification_ignored", "reason", "pagamento sem external_reference")
		return nil
	}

	_, err = uc.applier.apply(ctx, r.ExternalReference, r, in.CorrelationID)
	if errors.Is(err, domainerrors.ErrPaymentNotFound) || errors.Is(err, domainerrors.ErrBudgetNotFound) {
		log.Warn("notification_ignored", "reason", "external_reference não corresponde a pagamento do billing",
			"external_reference", r.ExternalReference)
		return nil
	}
	return err
}
