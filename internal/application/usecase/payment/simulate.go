package paymentuc

import (
	"context"
	"fmt"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// SimulatePayment simula o retorno do provedor no gateway fake (dev, BDD e demonstração). Passa
// pelo mesmo resultApplier do webhook real, então exercita as mesmas regras e eventos.
type SimulatePayment struct {
	payments ports.PaymentRepository
	applier  *resultApplier
	enabled  bool
}

func NewSimulatePayment(uow ports.UnitOfWork, payments ports.PaymentRepository, gateway ports.PaymentGateway) *SimulatePayment {
	return &SimulatePayment{
		payments: payments,
		applier:  &resultApplier{uow: uow, gateway: gateway, now: time.Now},
		enabled:  gateway.Provider() == entities.ProviderFake,
	}
}

func (uc *SimulatePayment) Execute(ctx context.Context, in ports.SimulatePaymentInput) (*entities.Payment, error) {
	if !uc.enabled {
		return nil, domainerrors.ErrSimulationDisabled
	}
	var status ports.ProviderStatus
	switch in.Status {
	case string(ports.ProviderStatusApproved):
		status = ports.ProviderStatusApproved
	case string(ports.ProviderStatusRejected):
		status = ports.ProviderStatusRejected
	default:
		return nil, fmt.Errorf("%w: status deve ser approved ou rejected", domainerrors.ErrInvalidInput)
	}

	p, err := uc.payments.FindByID(ctx, in.PaymentID)
	if err != nil {
		return nil, err
	}
	if p.Status() != entities.PaymentPending {
		return nil, fmt.Errorf("%w: só pagamentos pending podem ser simulados (atual: %s)",
			domainerrors.ErrInvalidTransition, p.Status())
	}
	return uc.applier.apply(ctx, p.ID(), ports.ProviderPayment{
		ID:                "fake-" + p.ID(),
		Status:            status,
		StatusDetail:      "simulated",
		Amount:            p.Amount(),
		ExternalReference: p.ID(),
	}, in.CorrelationID)
}
