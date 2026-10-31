package paymentuc

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// GetPayment consulta um pagamento (status e checkout_url) respeitando a posse do orçamento.
type GetPayment struct {
	payments ports.PaymentRepository
	budgets  ports.BudgetRepository
}

func NewGetPayment(payments ports.PaymentRepository, budgets ports.BudgetRepository) *GetPayment {
	return &GetPayment{payments: payments, budgets: budgets}
}

func (uc *GetPayment) Execute(ctx context.Context, id string, actor entities.Actor) (*entities.Payment, error) {
	p, err := uc.payments.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	b, err := uc.budgets.FindByID(ctx, p.BudgetID())
	if err != nil {
		return nil, err
	}
	if err := b.CheckReadAccess(actor); err != nil {
		return nil, err
	}
	return p, nil
}
