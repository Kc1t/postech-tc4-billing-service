package budgetuc

import (
	"context"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// ApproveBudget registra a aprovação do cliente dono e publica BudgetApproved.
type ApproveBudget struct {
	uow ports.UnitOfWork
	now func() time.Time
}

func NewApproveBudget(uow ports.UnitOfWork) *ApproveBudget {
	return &ApproveBudget{uow: uow, now: time.Now}
}

func (uc *ApproveBudget) Execute(ctx context.Context, in ports.DecideBudgetInput) (*entities.Budget, error) {
	var out *entities.Budget
	err := uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		b, err := tx.Budgets().FindByID(ctx, in.BudgetID)
		if err != nil {
			return err
		}
		if err := b.CheckDecisionAccess(in.Actor); err != nil {
			return err
		}
		if err := b.Approve(uc.now()); err != nil {
			return err
		}
		if err := tx.Budgets().Update(ctx, b); err != nil {
			return err
		}
		if err := tx.Events().Publish(ctx, contracts.EvtBudgetApproved, metaFor(b, in.CorrelationID),
			contracts.BudgetApproved{BudgetID: b.ID(), ApprovedAt: *b.DecidedAt()}); err != nil {
			return err
		}
		logger.FromContext(ctx).Info("budget_approved", "budget_id", b.ID(), "os_id", b.OSID(), "saga_id", b.SagaID())
		out = b
		return nil
	})
	return out, err
}
