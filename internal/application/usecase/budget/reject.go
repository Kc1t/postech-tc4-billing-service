package budgetuc

import (
	"context"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// RejectBudget registra a recusa do cliente dono e publica BudgetRejected.
type RejectBudget struct {
	uow ports.UnitOfWork
	now func() time.Time
}

func NewRejectBudget(uow ports.UnitOfWork) *RejectBudget {
	return &RejectBudget{uow: uow, now: time.Now}
}

func (uc *RejectBudget) Execute(ctx context.Context, in ports.DecideBudgetInput) (*entities.Budget, error) {
	var out *entities.Budget
	err := uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		b, err := tx.Budgets().FindByID(ctx, in.BudgetID)
		if err != nil {
			return err
		}
		if err := b.CheckDecisionAccess(in.Actor); err != nil {
			return err
		}
		if err := b.Reject(in.Reason, uc.now()); err != nil {
			return err
		}
		if err := tx.Budgets().Update(ctx, b); err != nil {
			return err
		}
		if err := tx.Events().Publish(ctx, contracts.EvtBudgetRejected, metaFor(b, in.CorrelationID),
			contracts.BudgetRejected{BudgetID: b.ID(), Reason: b.Reason()}); err != nil {
			return err
		}
		logger.FromContext(ctx).Info("budget_rejected", "budget_id", b.ID(), "os_id", b.OSID(), "saga_id", b.SagaID())
		out = b
		return nil
	})
	return out, err
}
