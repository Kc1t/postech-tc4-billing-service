package budgetuc

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

func cancelCmd(budgetID string) ports.CancelBudgetCommand {
	return ports.CancelBudgetCommand{Meta: meta, BudgetID: budgetID, Reason: "prazo de pagamento esgotado"}
}

func TestCancelBudget_SemPagamento(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetPendingApproval)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
	f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

	if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
		t.Fatal(err)
	}
	if b.Status() != entities.BudgetCancelled {
		t.Fatalf("status = %s", b.Status())
	}
	got := f.events.only(t, contracts.EvtBudgetCancelled).Payload.(contracts.BudgetCancelled)
	if got.BudgetID != b.ID() || got.Reason != "prazo de pagamento esgotado" {
		t.Fatalf("evento: %+v", got)
	}
}

func TestCancelBudget_CancelaCobrancaEmAberto(t *testing.T) {
	t.Run("sem pagamento no provedor: só local", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)
		f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if p.Status() != entities.PaymentCancelled || b.Status() != entities.BudgetCancelled {
			t.Fatalf("pagamento %s, orçamento %s", p.Status(), b.Status())
		}
	})

	t.Run("com pagamento em análise: cancela no provedor", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "mp-77")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.gateway.EXPECT().Cancel(gomock.Any(), "mp-77").Return(nil)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)
		f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		f.events.only(t, contracts.EvtBudgetCancelled)
	})

	t.Run("provedor recusa o cancelamento: segue local", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "mp-77")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.gateway.EXPECT().Cancel(gomock.Any(), "mp-77").Return(fmt.Errorf("%w: status 400", ports.ErrGatewayRejected))
		f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)
		f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("provedor fora: nova tentativa", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "mp-77")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.gateway.EXPECT().Cancel(gomock.Any(), "mp-77").Return(ports.ErrGatewayUnavailable)

		err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID()))
		if !errors.Is(err, ports.ErrGatewayUnavailable) || len(f.events.events) != 0 {
			t.Fatalf("esperava erro transitório sem eventos: %v", err)
		}
	})

	t.Run("pagamento aprovado não é cancelado (o estorno é outro comando)", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentApproved, "mp-1")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if p.Status() != entities.PaymentApproved {
			t.Fatal("pagamento aprovado foi alterado")
		}
	})
}

func TestCancelBudget_Idempotencia(t *testing.T) {
	t.Run("já cancelado confirma de novo", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetCancelled)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		f.events.only(t, contracts.EvtBudgetCancelled)
	})

	t.Run("recusado: nada a compensar", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetRejected)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); err != nil || len(f.events.events) != 0 {
			t.Fatalf("err=%v eventos=%+v", err, f.events.events)
		}
	})

	t.Run("inexistente: nada a compensar", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByID(gomock.Any(), "x").Return(nil, domainerrors.ErrBudgetNotFound)
		if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd("x")); err != nil || len(f.events.events) != 0 {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCancelBudget_ErrosTransitorios(t *testing.T) {
	boom := errors.New("db fora")
	cases := map[string]func(f *fixture, b *entities.Budget){
		"ler orçamento": func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(nil, boom)
		},
		"ler pagamento": func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, boom)
		},
		"salvar pagamento": func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(newPayment(t, b, entities.PaymentPending, ""), nil)
			f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(boom)
		},
		"salvar orçamento": func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
			f.budgets.EXPECT().Update(gomock.Any(), b).Return(boom)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetPendingApproval)
			setup(f, b)
			if err := NewCancelBudget(f.uow, f.gateway).Execute(context.Background(), cancelCmd(b.ID())); !errors.Is(err, boom) {
				t.Fatalf("esperava %v, veio %v", boom, err)
			}
		})
	}
}
