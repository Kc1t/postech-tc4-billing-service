// Package messaging é o adapter de entrada da fila billing-commands: traduz cada envelope de
// comando (pkg/contracts) para o comando do caso de uso e o executa dentro do inbox, o que torna
// o consumo idempotente — reentregas da mesma mensagem não repetem efeitos nem eventos.
//
// Erro devolvido por um handler mantém a mensagem na fila (nova tentativa e, esgotado o
// maxReceiveCount, DLQ). Por isso só falhas transitórias e mensagens malformadas viram erro;
// regra de negócio violada já sai do caso de uso como evento de falha.
package messaging

import (
	"context"
	"fmt"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	pkgmessaging "github.com/Kc1t/postech-tc4-billing-service/pkg/messaging"
)

// Registrar é onde os handlers são registrados (*messaging.Consumer do chassis).
type Registrar interface {
	Handle(msgType string, h pkgmessaging.Handler)
}

// CommandHandlers liga os comandos aos casos de uso.
type CommandHandlers struct {
	inbox          ports.Inbox
	createBudget   ports.CreateBudgetUseCase
	requestPayment ports.RequestPaymentUseCase
	cancelBudget   ports.CancelBudgetUseCase
	refundPayment  ports.RefundPaymentUseCase
}

func NewCommandHandlers(
	inbox ports.Inbox,
	createBudget ports.CreateBudgetUseCase,
	requestPayment ports.RequestPaymentUseCase,
	cancelBudget ports.CancelBudgetUseCase,
	refundPayment ports.RefundPaymentUseCase,
) *CommandHandlers {
	return &CommandHandlers{
		inbox:          inbox,
		createBudget:   createBudget,
		requestPayment: requestPayment,
		cancelBudget:   cancelBudget,
		refundPayment:  refundPayment,
	}
}

// Register registra os quatro comandos do billing.
func (h *CommandHandlers) Register(r Registrar) {
	r.Handle(contracts.CmdCreateBudget, h.CreateBudget)
	r.Handle(contracts.CmdRequestPayment, h.RequestPayment)
	r.Handle(contracts.CmdCancelBudget, h.CancelBudget)
	r.Handle(contracts.CmdRefundPayment, h.RefundPayment)
}

func (h *CommandHandlers) CreateBudget(ctx context.Context, env contracts.Envelope) error {
	cmd, err := decode[contracts.CreateBudget](ctx, env)
	if err != nil {
		return err
	}
	items := make([]entities.BudgetItem, len(cmd.Items))
	for i, it := range cmd.Items {
		items[i] = entities.BudgetItem{
			Kind: entities.ItemKind(it.Kind), Ref: it.Ref, Description: it.Description,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice,
		}
	}
	in := ports.CreateBudgetCommand{
		Meta:   env.Reply(),
		OSCode: cmd.OSCode,
		Requester: entities.Requester{
			ID: cmd.Requester.ID, Name: cmd.Requester.Name, Email: cmd.Requester.Email, Document: cmd.Requester.Document,
		},
		Items:           items,
		Total:           cmd.Total,
		SimulateFailure: cmd.SimulateFailure,
	}
	return h.once(ctx, env, func(ctx context.Context) error { return h.createBudget.Execute(ctx, in) })
}

func (h *CommandHandlers) RequestPayment(ctx context.Context, env contracts.Envelope) error {
	cmd, err := decode[contracts.RequestPayment](ctx, env)
	if err != nil {
		return err
	}
	in := ports.RequestPaymentCommand{Meta: env.Reply(), BudgetID: cmd.BudgetID, SimulateFailure: cmd.SimulateFailure}
	return h.once(ctx, env, func(ctx context.Context) error { return h.requestPayment.Execute(ctx, in) })
}

func (h *CommandHandlers) CancelBudget(ctx context.Context, env contracts.Envelope) error {
	cmd, err := decode[contracts.CancelBudget](ctx, env)
	if err != nil {
		return err
	}
	in := ports.CancelBudgetCommand{Meta: env.Reply(), BudgetID: cmd.BudgetID, Reason: cmd.Reason}
	return h.once(ctx, env, func(ctx context.Context) error { return h.cancelBudget.Execute(ctx, in) })
}

func (h *CommandHandlers) RefundPayment(ctx context.Context, env contracts.Envelope) error {
	cmd, err := decode[contracts.RefundPayment](ctx, env)
	if err != nil {
		return err
	}
	in := ports.RefundPaymentCommand{Meta: env.Reply(), BudgetID: cmd.BudgetID, Reason: cmd.Reason}
	return h.once(ctx, env, func(ctx context.Context) error { return h.refundPayment.Execute(ctx, in) })
}

func (h *CommandHandlers) once(ctx context.Context, env contracts.Envelope, fn func(ctx context.Context) error) error {
	duplicate, err := h.inbox.Once(ctx, env, fn)
	if duplicate {
		logger.FromContext(ctx).Info("message_duplicate_skipped")
	}
	return err
}

// decode lê o payload. Payload que não corresponde ao contrato é erro: a mensagem segue o mesmo
// caminho de um envelope malformado no chassis (tentativas e DLQ, onde fica para análise).
func decode[T any](ctx context.Context, env contracts.Envelope) (T, error) {
	v, err := contracts.Decode[T](env)
	if err != nil {
		logger.FromContext(ctx).Error("payload_malformed", "error", err)
		return v, fmt.Errorf("messaging: %w", err)
	}
	return v, nil
}
