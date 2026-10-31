// Package ports define as portas da arquitetura hexagonal: o que o núcleo (casos de uso) espera
// do mundo de fora (repositórios, transação, outbox, gateway de pagamento) e o que oferece aos
// adapters de entrada (interfaces dos casos de uso).
package ports

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
)

//go:generate mockgen -source=./repositories.go -destination=./mocks/repositories.go -package=mocks

// BudgetRepository persiste o agregado Budget.
type BudgetRepository interface {
	// Create grava um orçamento novo. Devolve ErrBudgetAlreadyExists se a OS já tem orçamento.
	Create(ctx context.Context, b *entities.Budget) error
	// FindByID devolve ErrBudgetNotFound se não existir.
	FindByID(ctx context.Context, id string) (*entities.Budget, error)
	// FindByOSID devolve ErrBudgetNotFound se a OS não tiver orçamento.
	FindByOSID(ctx context.Context, osID string) (*entities.Budget, error)
	// Update grava as mudanças com optimistic lock: devolve ErrConcurrentUpdate se a version
	// mudou desde a leitura. Em caso de sucesso, a version do agregado é incrementada.
	Update(ctx context.Context, b *entities.Budget) error
}

// PaymentRepository persiste o agregado Payment.
type PaymentRepository interface {
	Create(ctx context.Context, p *entities.Payment) error
	// FindByID devolve ErrPaymentNotFound se não existir.
	FindByID(ctx context.Context, id string) (*entities.Payment, error)
	// FindLatestByBudgetID devolve a tentativa de cobrança mais recente do orçamento, ou
	// ErrPaymentNotFound se o orçamento nunca foi cobrado.
	FindLatestByBudgetID(ctx context.Context, budgetID string) (*entities.Payment, error)
	// Update grava as mudanças com optimistic lock (ErrConcurrentUpdate em conflito).
	Update(ctx context.Context, p *entities.Payment) error
}
