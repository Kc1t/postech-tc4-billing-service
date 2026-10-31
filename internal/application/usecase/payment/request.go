// Package paymentuc reúne os casos de uso do agregado pagamento.
package paymentuc

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

// RequestPayment trata billing.RequestPayment: cria a cobrança do orçamento aprovado e a
// preferência no provedor, e responde PaymentLinkCreated com o link de checkout.
//
// Regras violadas (orçamento inexistente ou não aprovado), recusa definitiva do provedor e
// falha simulada viram PaymentFailed. Falha transitória do provedor devolve erro: a transação
// é desfeita e a mensagem volta para a fila. É idempotente por orçamento: se já há cobrança em
// aberto ou aprovada, responde de novo com ela em vez de criar outra.
//
// A chamada ao provedor acontece dentro da transação (a do inbox). Se o commit falhar depois
// de a preferência ser criada, a reentrega cria outra; a anterior expira sem pagamento.
type RequestPayment struct {
	uow            ports.UnitOfWork
	gateway        ports.PaymentGateway
	faultInjection bool
	now            func() time.Time
}

func NewRequestPayment(uow ports.UnitOfWork, gateway ports.PaymentGateway, faultInjection bool) *RequestPayment {
	return &RequestPayment{uow: uow, gateway: gateway, faultInjection: faultInjection, now: time.Now}
}

func (uc *RequestPayment) Execute(ctx context.Context, cmd ports.RequestPaymentCommand) error {
	log := logger.FromContext(ctx).With("budget_id", cmd.BudgetID)
	return uc.uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		b, err := tx.Budgets().FindByID(ctx, cmd.BudgetID)
		if errors.Is(err, domainerrors.ErrBudgetNotFound) {
			return publishFailed(ctx, tx, cmd.Meta, cmd.BudgetID, "", "orçamento não encontrado")
		}
		if err != nil {
			return err
		}

		if latest, err := tx.Payments().FindLatestByBudgetID(ctx, b.ID()); err == nil {
			if done, err := replyExisting(ctx, tx, cmd.Meta, latest); done || err != nil {
				return err
			}
		} else if !errors.Is(err, domainerrors.ErrPaymentNotFound) {
			return err
		}

		p, err := entities.NewPayment(b, uc.gateway.Provider(), uc.now())
		if errors.Is(err, domainerrors.ErrBudgetNotApproved) {
			return publishFailed(ctx, tx, cmd.Meta, b.ID(), "", err.Error())
		}
		if err != nil {
			return err
		}
		if err := tx.Payments().Create(ctx, p); err != nil {
			return err
		}
		log = log.With("payment_id", p.ID())

		if uc.faultInjection && cmd.SimulateFailure == contracts.FailPayment {
			return uc.fail(ctx, tx, cmd.Meta, p, "falha simulada no pagamento (simulate_failure=payment)")
		}

		prefID, url, err := uc.gateway.CreateCheckout(ctx, ports.CheckoutRequest{
			PaymentID:  p.ID(),
			BudgetID:   b.ID(),
			OSCode:     b.OSCode(),
			Amount:     p.Amount(),
			PayerEmail: b.Requester().Email,
			PayerName:  b.Requester().Name,
		})
		if errors.Is(err, ports.ErrGatewayRejected) {
			return uc.fail(ctx, tx, cmd.Meta, p, "provedor recusou a criação do checkout: "+err.Error())
		}
		if err != nil {
			return fmt.Errorf("criar checkout do pagamento %s: %w", p.ID(), err)
		}

		if err := p.LinkCreated(prefID, url, uc.now()); err != nil {
			return err
		}
		if err := tx.Payments().Update(ctx, p); err != nil {
			return err
		}
		log.Info("payment_link_created", "provider", string(p.Provider()), "amount", p.Amount())
		return publishLink(ctx, tx, cmd.Meta, p)
	})
}

// replyExisting responde pela cobrança que já existe quando ela ainda vale (em aberto ou paga).
// Devolve done=false se a cobrança anterior terminou sem pagamento e uma nova pode ser criada.
func replyExisting(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment) (bool, error) {
	log := logger.FromContext(ctx).With("payment_id", p.ID(), "status", string(p.Status()))
	switch p.Status() {
	case entities.PaymentPending:
		log.Info("payment_already_requested")
		return true, publishLink(ctx, tx, meta, p)
	case entities.PaymentApproved:
		log.Info("payment_already_approved")
		return true, publishApproved(ctx, tx, meta, p)
	}
	return false, nil
}

func (uc *RequestPayment) fail(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment, reason string) error {
	if err := p.Fail(reason, "", uc.now()); err != nil {
		return err
	}
	if err := tx.Payments().Update(ctx, p); err != nil {
		return err
	}
	return publishFailed(ctx, tx, meta, p.BudgetID(), p.ID(), reason)
}

func publishLink(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment) error {
	return tx.Events().Publish(ctx, contracts.EvtPaymentLinkCreated, meta, contracts.PaymentLinkCreated{
		BudgetID: p.BudgetID(), PaymentID: p.ID(), CheckoutURL: p.CheckoutURL(),
	})
}

func publishApproved(ctx context.Context, tx ports.Tx, meta contracts.Meta, p *entities.Payment) error {
	return tx.Events().Publish(ctx, contracts.EvtPaymentApproved, meta, contracts.PaymentApproved{
		BudgetID: p.BudgetID(), PaymentID: p.ID(), ProviderPaymentID: p.ProviderPaymentID(), Amount: p.Amount(),
	})
}

func publishFailed(ctx context.Context, tx ports.Tx, meta contracts.Meta, budgetID, paymentID, reason string) error {
	logger.FromContext(ctx).Warn("payment_failed", "budget_id", budgetID, "payment_id", paymentID, "reason", reason)
	return tx.Events().Publish(ctx, contracts.EvtPaymentFailed, meta, contracts.PaymentFailed{
		BudgetID: budgetID, PaymentID: paymentID, Reason: reason,
	})
}
