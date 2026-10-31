package paymentuc

import (
	"context"
	"errors"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

func refundCmd(budgetID string) ports.RefundPaymentCommand {
	return ports.RefundPaymentCommand{Meta: meta, BudgetID: budgetID, Reason: "reparo rejeitado"}
}

func TestRefundPayment_Estorna(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentApproved, "mp-1")
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
	// A chave de idempotência é o ID do pagamento.
	f.gateway.EXPECT().Refund(gomock.Any(), "mp-1", p.ID()).Return(nil)
	f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

	if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd(b.ID())); err != nil {
		t.Fatal(err)
	}
	if p.Status() != entities.PaymentRefunded || p.RefundedAt() == nil {
		t.Fatalf("pagamento: %+v", p.Snapshot())
	}
	got := f.events.only(t, contracts.EvtPaymentRefunded).Payload.(contracts.PaymentRefunded)
	if got.PaymentID != p.ID() || got.Amount != 151 || got.BudgetID != b.ID() {
		t.Fatalf("payload: %+v", got)
	}
}

func TestRefundPayment_Idempotencia(t *testing.T) {
	t.Run("já estornado confirma de novo", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(newPayment(t, b, entities.PaymentRefunded, "mp-1"), nil)
		if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		f.events.only(t, contracts.EvtPaymentRefunded)
	})
	t.Run("sem pagamento", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), "b").Return(nil, domainerrors.ErrPaymentNotFound)
		if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd("b")); err != nil {
			t.Fatal(err)
		}
		f.events.none(t)
	})
	t.Run("pagamento não aprovado", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(newPayment(t, b, entities.PaymentCancelled, ""), nil)
		if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		f.events.none(t)
	})
}

func TestRefundPayment_Erros(t *testing.T) {
	boom := errors.New("db fora")
	for name, gwErr := range map[string]error{
		"provedor fora":   ports.ErrGatewayUnavailable,
		"provedor recusa": ports.ErrGatewayRejected,
	} {
		t.Run(name+" vai para retry/DLQ", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetApproved)
			p := newPayment(t, b, entities.PaymentApproved, "mp-1")
			f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
			f.gateway.EXPECT().Refund(gomock.Any(), "mp-1", p.ID()).Return(gwErr)
			if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd(b.ID())); !errors.Is(err, gwErr) {
				t.Fatalf("veio %v", err)
			}
			if p.Status() != entities.PaymentApproved {
				t.Fatal("não pode marcar estornado sem estorno")
			}
			f.events.none(t)
		})
	}
	t.Run("ler pagamento", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), "b").Return(nil, boom)
		if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd("b")); !errors.Is(err, boom) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("salvar", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentApproved, "mp-1")
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.gateway.EXPECT().Refund(gomock.Any(), "mp-1", p.ID()).Return(nil)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(boom)
		if err := NewRefundPayment(f.uow, f.gateway).Execute(context.Background(), refundCmd(b.ID())); !errors.Is(err, boom) {
			t.Fatalf("veio %v", err)
		}
	})
}
