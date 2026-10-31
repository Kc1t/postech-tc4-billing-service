package budgetuc

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// GetBudgetByOS consulta o orçamento de uma OS. É a rota usada pela API composition do
// os-service (GET /service-orders/{id}/details).
type GetBudgetByOS struct {
	budgets  ports.BudgetRepository
	payments ports.PaymentRepository
}

func NewGetBudgetByOS(budgets ports.BudgetRepository, payments ports.PaymentRepository) *GetBudgetByOS {
	return &GetBudgetByOS{budgets: budgets, payments: payments}
}

func (uc *GetBudgetByOS) Execute(ctx context.Context, osID string, actor entities.Actor) (ports.BudgetView, error) {
	b, err := uc.budgets.FindByOSID(ctx, osID)
	if err != nil {
		return ports.BudgetView{}, err
	}
	return view(ctx, uc.payments, actor, b)
}
