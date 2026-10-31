package budgetuc

import (
	"context"
	"errors"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// GetBudgetByID consulta um orçamento (com a cobrança mais recente) respeitando a posse.
type GetBudgetByID struct {
	budgets  ports.BudgetRepository
	payments ports.PaymentRepository
}

func NewGetBudgetByID(budgets ports.BudgetRepository, payments ports.PaymentRepository) *GetBudgetByID {
	return &GetBudgetByID{budgets: budgets, payments: payments}
}

func (uc *GetBudgetByID) Execute(ctx context.Context, id string, actor entities.Actor) (ports.BudgetView, error) {
	b, err := uc.budgets.FindByID(ctx, id)
	if err != nil {
		return ports.BudgetView{}, err
	}
	return view(ctx, uc.payments, actor, b)
}

func view(ctx context.Context, payments ports.PaymentRepository, actor entities.Actor, b *entities.Budget) (ports.BudgetView, error) {
	if err := b.CheckReadAccess(actor); err != nil {
		return ports.BudgetView{}, err
	}
	p, err := payments.FindLatestByBudgetID(ctx, b.ID())
	if err != nil && !errors.Is(err, domainerrors.ErrPaymentNotFound) {
		return ports.BudgetView{}, err
	}
	return ports.BudgetView{Budget: b, Payment: p}, nil
}
