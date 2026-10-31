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

// RefundPayment trata billing.RefundPayment, a compensação do passo "pagamento" da saga:
// estorna no provedor o pagamento aprovado do orçamento e responde PaymentRefunded.
//
// A chave de idempotência do estorno é o ID do pagamento, então reentregas (ou um commit que
// falhou depois do estorno) não estornam duas vezes. Um estorno recusado pelo provedor não é
// regra de negócio do billing: exige intervenção humana, então a mensagem segue para a DLQ.
type RefundPayment struct {
	uow     ports.UnitOfWork
	gateway ports.PaymentGateway
	now     func() time.Time
}

func NewRefundPayment(uow ports.UnitOfWork, gateway ports.PaymentGateway) *RefundPayment {
	return &RefundPayment{uow: uow, gateway: gateway, now: time.Now}
}

func (uc *RefundPayment) Execute(ctx context.Context, cmd ports.RefundPaymentCommand) error {
	log := logger.FromContext(ctx).With("budget_id", cmd.BudgetID)
	return uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := tx.Payments().FindLatestByBudgetID(ctx, cmd.BudgetID)
		if errors.Is(err, domainerrors.ErrPaymentNotFound) {
			log.Warn("refund_ignored", "reason", "orçamento sem pagamento")
			return nil
		}
		if err != nil {
			return err
		}
		log = log.With("payment_id", p.ID())

		switch p.Status() {
		case entities.PaymentRefunded:
			log.Info("payment_already_refunded")
			return publishRefunded(ctx, tx, cmd.Meta, p)
		case entities.PaymentApproved:
		default:
			log.Warn("refund_ignored", "reason", "pagamento não aprovado", "status", string(p.Status()))
			return nil
		}

		if err := uc.gateway.Refund(ctx, p.ProviderPaymentID(), p.ID()); err != nil {
			log.Error("refund_failed", "error", err, "definitive", errors.Is(err, ports.ErrGatewayRejected))
			return fmt.Errorf("estornar pagamento %s: %w", p.ID(), err)
		}
		if err := p.Refund(uc.now()); err != nil {
			return err
		}
		if err := tx.Payments().Update(ctx, p); err != nil {
			return err
		}
		log.Info("payment_refunded", "amount", p.Amount(), "reason", cmd.Reason)
		return publishRefunded(ctx, tx, cmd.Meta, p)
	})
}

func publishRefunded(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment) error {
	return tx.Events().Publish(ctx, contracts.EvtPaymentRefunded, meta, contracts.PaymentRefunded{
		BudgetID: p.BudgetID(), PaymentID: p.ID(), Amount: p.Amount(),
	})
}
