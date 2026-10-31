package ports

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
)

//go:generate mockgen -source=./usecases.go -destination=./mocks/usecases.go -package=mocks

// ---- Comandos consumidos da fila billing-commands ----

// CreateBudgetCommand é o comando billing.CreateBudget já traduzido do envelope.
type CreateBudgetCommand struct {
	Meta            contracts.Meta // saga/OS/correlação para os eventos de resposta
	OSCode          int
	Requester       entities.Requester
	Items           []entities.BudgetItem
	Total           float64
	SimulateFailure string
}

// RequestPaymentCommand é o comando billing.RequestPayment.
type RequestPaymentCommand struct {
	Meta            contracts.Meta
	BudgetID        string
	SimulateFailure string
}

// CancelBudgetCommand é o comando billing.CancelBudget (compensação).
type CancelBudgetCommand struct {
	Meta     contracts.Meta
	BudgetID string
	Reason   string
}

// RefundPaymentCommand é o comando billing.RefundPayment (compensação).
type RefundPaymentCommand struct {
	Meta     contracts.Meta
	BudgetID string
	Reason   string
}

type CreateBudgetUseCase interface {
	Execute(ctx context.Context, cmd CreateBudgetCommand) error
}

type RequestPaymentUseCase interface {
	Execute(ctx context.Context, cmd RequestPaymentCommand) error
}

type CancelBudgetUseCase interface {
	Execute(ctx context.Context, cmd CancelBudgetCommand) error
}

type RefundPaymentUseCase interface {
	Execute(ctx context.Context, cmd RefundPaymentCommand) error
}

// ---- Casos de uso da API ----

// BudgetView é o orçamento com a tentativa de cobrança mais recente (nil se ainda não cobrado).
type BudgetView struct {
	Budget  *entities.Budget
	Payment *entities.Payment
}

// DecideBudgetInput é a decisão do cliente sobre o orçamento.
type DecideBudgetInput struct {
	BudgetID      string
	Actor         entities.Actor
	Reason        string // obrigatório na recusa
	CorrelationID string
}

// SimulatePaymentInput simula o retorno do provedor (gateway fake).
type SimulatePaymentInput struct {
	PaymentID     string
	Status        string // "approved" | "rejected"
	CorrelationID string
}

// PaymentNotificationInput é a notificação de pagamento já autenticada pelo adapter de webhook.
type PaymentNotificationInput struct {
	ProviderPaymentID string
	CorrelationID     string
}

type GetBudgetByIDUseCase interface {
	Execute(ctx context.Context, id string, actor entities.Actor) (BudgetView, error)
}

type GetBudgetByOSUseCase interface {
	Execute(ctx context.Context, osID string, actor entities.Actor) (BudgetView, error)
}

type ApproveBudgetUseCase interface {
	Execute(ctx context.Context, in DecideBudgetInput) (*entities.Budget, error)
}

type RejectBudgetUseCase interface {
	Execute(ctx context.Context, in DecideBudgetInput) (*entities.Budget, error)
}

type GetPaymentUseCase interface {
	Execute(ctx context.Context, id string, actor entities.Actor) (*entities.Payment, error)
}

type SimulatePaymentUseCase interface {
	Execute(ctx context.Context, in SimulatePaymentInput) (*entities.Payment, error)
}

type ProcessPaymentNotificationUseCase interface {
	Execute(ctx context.Context, in PaymentNotificationInput) error
}
