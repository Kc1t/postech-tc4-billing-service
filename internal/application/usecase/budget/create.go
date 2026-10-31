// Package budgetuc reúne os casos de uso do agregado orçamento.
package budgetuc

import (
	"context"
	"errors"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// CreateBudget trata billing.CreateBudget: abre o orçamento da OS e responde BudgetCreated, ou
// BudgetCreationFailed quando o comando viola as regras de criação (ou a falha é simulada).
//
// É idempotente por os_id: se a OS já tem orçamento, responde de novo BudgetCreated com ele.
type CreateBudget struct {
	uow            ports.UnitOfWork
	faultInjection bool
	now            func() time.Time
}

func NewCreateBudget(uow ports.UnitOfWork, faultInjection bool) *CreateBudget {
	return &CreateBudget{uow: uow, faultInjection: faultInjection, now: time.Now}
}

func (uc *CreateBudget) Execute(ctx context.Context, cmd ports.CreateBudgetCommand) error {
	log := logger.FromContext(ctx)
	return uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		if uc.faultInjection && cmd.SimulateFailure == contracts.FailBudget {
			return uc.fail(ctx, tx, cmd, "falha simulada na criação do orçamento (simulate_failure=budget)")
		}

		existing, err := tx.Budgets().FindByOSID(ctx, cmd.Meta.OSID)
		switch {
		case err == nil:
			log.Info("budget_already_exists", "budget_id", existing.ID(), "status", string(existing.Status()))
			return publishCreated(ctx, tx, cmd.Meta, existing)
		case !errors.Is(err, domainerrors.ErrBudgetNotFound):
			return err
		}

		b, err := entities.NewBudget(entities.NewBudgetInput{
			OSID:      cmd.Meta.OSID,
			OSCode:    cmd.OSCode,
			SagaID:    cmd.Meta.SagaID,
			Requester: cmd.Requester,
			Items:     cmd.Items,
			Total:     cmd.Total,
		}, uc.now())
		if errors.Is(err, domainerrors.ErrInvalidBudget) {
			return uc.fail(ctx, tx, cmd, err.Error())
		}
		if err != nil {
			return err
		}
		// Uma corrida com outra entrega do mesmo comando estoura a unicidade de os_id: o erro
		// desfaz a transação e a nova tentativa cai no caminho idempotente acima.
		if err := tx.Budgets().Create(ctx, b); err != nil {
			return err
		}
		log.Info("budget_created", "budget_id", b.ID(), "total", b.Total(), "items", len(b.Items()),
			"requester_document", entities.MaskDocument(b.Requester().Document))
		return publishCreated(ctx, tx, cmd.Meta, b)
	})
}

func (uc *CreateBudget) fail(ctx context.Context, tx ports.Tx, cmd ports.CreateBudgetCommand, reason string) error {
	logger.FromContext(ctx).Warn("budget_creation_failed", "reason", reason)
	return tx.Events().Publish(ctx, contracts.EvtBudgetCreationFailed, cmd.Meta, contracts.BudgetCreationFailed{Reason: reason})
}

func publishCreated(ctx context.Context, tx ports.Tx, meta contracts.Meta, b *entities.Budget) error {
	return tx.Events().Publish(ctx, contracts.EvtBudgetCreated, meta, contracts.BudgetCreated{BudgetID: b.ID(), Total: b.Total()})
}
