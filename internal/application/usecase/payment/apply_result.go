package paymentuc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// resultApplier aplica ao nosso pagamento o estado que o provedor informou. É o núcleo comum do
// webhook do Mercado Pago (depois de reler o pagamento) e do simulador do gateway fake.
//
// Mapeamento (nota técnica do MP, seção 3):
//   - approved → PaymentApproved (o valor pago precisa ser o cobrado);
//   - rejected/cancelled → PaymentFailed. No Checkout Pro o cliente poderia tentar de novo,
//     mas para a saga a recusa é definitiva;
//   - pending/in_process/authorized → só guarda o ID do pagamento no provedor;
//   - refunded/charged_back/in_mediation → nada (o estorno é comandado pela saga).
//
// É idempotente: notificações repetidas (payment.created e payment.updated, reentregas) não
// geram eventos duplicados porque a máquina de estados não repete transições.
//
// Pagamento "órfão" — aprovado no provedor quando a cobrança já terminou (orçamento cancelado,
// tentativa recusada antes, segundo pagamento da mesma preferência, valor divergente) — é
// estornado automaticamente: nenhum cliente fica cobrado por uma OS que não vai ser executada.
type resultApplier struct {
	uow     ports.UnitOfWork
	gateway ports.PaymentGateway
	now     func() time.Time
}

func (a *resultApplier) apply(ctx context.Context, paymentID string, r ports.ProviderPayment, correlationID string) (*entities.Payment, error) {
	var (
		out    *entities.Payment
		orphan bool
	)
	err := a.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Payments().FindByID(ctx, paymentID)
		if err != nil {
			return err
		}
		b, err := tx.Budgets().FindByID(ctx, p.BudgetID())
		if err != nil {
			return err
		}
		out = p
		meta := contracts.Meta{SagaID: b.SagaID(), OSID: b.OSID(), CorrelationID: correlationID}
		log := logger.FromContext(ctx).With("payment_id", p.ID(), "budget_id", p.BudgetID(),
			"os_id", b.OSID(), "saga_id", b.SagaID(), "provider_payment_id", r.ID, "provider_status", string(r.Status))

		switch r.Status {
		case ports.ProviderStatusApproved:
			orphan, err = a.approve(ctx, tx, meta, p, r)
			return err
		case ports.ProviderStatusRejected, ports.ProviderStatusCancelled:
			if !p.IsOpen() {
				log.Info("provider_update_ignored", "status", string(p.Status()))
				return nil
			}
			reason := fmt.Sprintf("pagamento %s pelo provedor", r.Status)
			if r.StatusDetail != "" {
				reason += " (" + r.StatusDetail + ")"
			}
			return a.fail(ctx, tx, meta, p, reason, r.ID)
		case ports.ProviderStatusPending, ports.ProviderStatusInProcess, ports.ProviderStatusAuthorized:
			if p.AttachProviderPayment(r.ID, a.now()) {
				log.Info("payment_in_analysis")
				return tx.Payments().Update(ctx, p)
			}
			return nil
		default:
			log.Info("provider_update_ignored", "status", string(p.Status()))
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	if orphan {
		if err := a.refundOrphan(ctx, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// approve devolve orphan=true quando o pagamento aprovado no provedor não pode ser aceito.
func (a *resultApplier) approve(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment, r ports.ProviderPayment) (bool, error) {
	if r.ID == p.ProviderPaymentID() && !p.IsOpen() {
		return false, nil // reentrega de algo já aplicado
	}
	if !p.IsOpen() {
		return true, nil
	}
	err := p.Approve(r.ID, r.Amount, a.now())
	if errors.Is(err, domainerrors.ErrAmountMismatch) {
		return true, a.fail(ctx, tx, meta, p, err.Error(), r.ID)
	}
	if err != nil {
		return false, err
	}
	if err := tx.Payments().Update(ctx, p); err != nil {
		return false, err
	}
	logger.FromContext(ctx).Info("payment_approved", "payment_id", p.ID(), "provider_payment_id", r.ID, "amount", p.Amount())
	return false, publishApproved(ctx, tx, meta, p)
}

func (a *resultApplier) fail(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment, reason, providerPaymentID string) error {
	if err := p.Fail(reason, providerPaymentID, a.now()); err != nil {
		return err
	}
	if err := tx.Payments().Update(ctx, p); err != nil {
		return err
	}
	return publishFailed(ctx, tx, meta, p.BudgetID(), p.ID(), reason)
}

func (a *resultApplier) refundOrphan(ctx context.Context, r ports.ProviderPayment) error {
	log := logger.FromContext(ctx).With("provider_payment_id", r.ID, "external_reference", r.ExternalReference)
	if err := a.gateway.Refund(ctx, r.ID, "orphan-"+r.ID); err != nil {
		log.Error("orphan_payment_refund_failed", "error", err)
		return fmt.Errorf("estornar pagamento órfão %s: %w", r.ID, err)
	}
	log.Warn("orphan_payment_refunded", "amount", r.Amount)
	return nil
}
