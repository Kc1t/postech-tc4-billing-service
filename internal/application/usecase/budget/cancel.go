package budgetuc

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

// CancelBudget trata billing.CancelBudget, a compensação do passo "orçamento" da saga: o
// orçamento vai para cancelled e a cobrança em aberto (se houver) também é cancelada — no
// provedor, quando ele já registrou um pagamento em análise.
//
// Compensações precisam ser idempotentes: orçamento já cancelado só confirma de novo com
// BudgetCancelled; orçamento recusado ou inexistente não tem o que compensar.
type CancelBudget struct {
	uow     ports.UnitOfWork
	gateway ports.PaymentGateway
	now     func() time.Time
}

func NewCancelBudget(uow ports.UnitOfWork, gateway ports.PaymentGateway) *CancelBudget {
	return &CancelBudget{uow: uow, gateway: gateway, now: time.Now}
}

func (uc *CancelBudget) Execute(ctx context.Context, cmd ports.CancelBudgetCommand) error {
	log := logger.FromContext(ctx).With("budget_id", cmd.BudgetID)
	return uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		b, err := tx.Budgets().FindByID(ctx, cmd.BudgetID)
		if errors.Is(err, domainerrors.ErrBudgetNotFound) {
			log.Warn("budget_cancel_ignored", "reason", "orçamento inexistente")
			return nil
		}
		if err != nil {
			return err
		}

		switch b.Status() {
		case entities.BudgetCancelled:
			log.Info("budget_already_cancelled")
			return publishCancelled(ctx, tx, cmd.Meta, b)
		case entities.BudgetRejected:
			log.Info("budget_cancel_ignored", "reason", "orçamento já recusado pelo cliente")
			return nil
		}

		if err := uc.cancelOpenPayment(ctx, tx, b, cmd.Reason); err != nil {
			return err
		}
		if err := b.Cancel(cmd.Reason, uc.now()); err != nil {
			return err
		}
		if err := tx.Budgets().Update(ctx, b); err != nil {
			return err
		}
		log.Info("budget_cancelled", "reason", cmd.Reason)
		return publishCancelled(ctx, tx, cmd.Meta, b)
	})
}

func (uc *CancelBudget) cancelOpenPayment(ctx context.Context, tx ports.Tx, b *entities.Budget, reason string) error {
	p, err := tx.Payments().FindLatestByBudgetID(ctx, b.ID())
	if errors.Is(err, domainerrors.ErrPaymentNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !p.IsOpen() {
		return nil
	}
	log := logger.FromContext(ctx).With("payment_id", p.ID())
	if id := p.ProviderPaymentID(); id != "" {
		if err := uc.gateway.Cancel(ctx, id); err != nil {
			if errors.Is(err, ports.ErrGatewayUnavailable) {
				return fmt.Errorf("cancelar pagamento %s no provedor: %w", p.ID(), err)
			}
			// Recusa definitiva (ex.: o provedor já encerrou o pagamento): não há o que cancelar lá.
			log.Warn("provider_cancel_skipped", "error", err)
		}
	}
	if err := p.Cancel("orçamento cancelado: "+reason, uc.now()); err != nil {
		return err
	}
	if err := tx.Payments().Update(ctx, p); err != nil {
		return err
	}
	log.Info("payment_cancelled")
	return nil
}

func publishCancelled(ctx context.Context, tx ports.Tx, meta contracts.Meta, b *entities.Budget) error {
	return tx.Events().Publish(ctx, contracts.EvtBudgetCancelled, meta, contracts.BudgetCancelled{BudgetID: b.ID(), Reason: b.Reason()})
}
