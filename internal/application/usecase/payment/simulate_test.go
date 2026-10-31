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

func newSimulator(f *fixture, provider entities.Provider) *SimulatePayment {
	f.gateway.EXPECT().Provider().Return(provider)
	return NewSimulatePayment(f.uow, f.payments, f.gateway)
}

func TestSimulatePayment_Aprovado(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	uc := newSimulator(f, entities.ProviderFake)
	f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil).Times(2)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

	got, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: p.ID(), Status: "approved", CorrelationID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != entities.PaymentApproved || got.ProviderPaymentID() != "fake-"+p.ID() {
		t.Fatalf("pagamento: %+v", got.Snapshot())
	}
	f.events.only(t, contracts.EvtPaymentApproved)
}

func TestSimulatePayment_Recusado(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	uc := newSimulator(f, entities.ProviderFake)
	f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil).Times(2)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

	got, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: p.ID(), Status: "rejected"})
	if err != nil || got.Status() != entities.PaymentFailed {
		t.Fatalf("err=%v", err)
	}
	f.events.only(t, contracts.EvtPaymentFailed)
}

func TestSimulatePayment_Erros(t *testing.T) {
	t.Run("gateway real", func(t *testing.T) {
		f := newFixture(t)
		uc := newSimulator(f, entities.ProviderMercadoPago)
		if _, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: "p", Status: "approved"}); !errors.Is(err, domainerrors.ErrSimulationDisabled) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("status inválido", func(t *testing.T) {
		f := newFixture(t)
		uc := newSimulator(f, entities.ProviderFake)
		if _, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: "p", Status: "pending"}); !errors.Is(err, domainerrors.ErrInvalidInput) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("inexistente", func(t *testing.T) {
		f := newFixture(t)
		uc := newSimulator(f, entities.ProviderFake)
		f.payments.EXPECT().FindByID(gomock.Any(), "p").Return(nil, domainerrors.ErrPaymentNotFound)
		if _, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: "p", Status: "approved"}); !errors.Is(err, domainerrors.ErrPaymentNotFound) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("não pendente", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentApproved, "x")
		uc := newSimulator(f, entities.ProviderFake)
		f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
		if _, err := uc.Execute(context.Background(), ports.SimulatePaymentInput{PaymentID: p.ID(), Status: "approved"}); !errors.Is(err, domainerrors.ErrInvalidTransition) {
			t.Fatalf("veio %v", err)
		}
	})
}

func TestGetPayment(t *testing.T) {
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	owner := entities.Actor{Role: entities.RoleClient, Document: ownerDoc}

	t.Run("dono", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		got, err := NewGetPayment(f.payments, f.budgets).Execute(context.Background(), p.ID(), owner)
		if err != nil || got != p {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("outro cliente", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		other := entities.Actor{Role: entities.RoleClient, Document: "11144477735"}
		if _, err := NewGetPayment(f.payments, f.budgets).Execute(context.Background(), p.ID(), other); !errors.Is(err, domainerrors.ErrNotOwner) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("inexistente", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindByID(gomock.Any(), "x").Return(nil, domainerrors.ErrPaymentNotFound)
		if _, err := NewGetPayment(f.payments, f.budgets).Execute(context.Background(), "x", owner); !errors.Is(err, domainerrors.ErrPaymentNotFound) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("orçamento some", func(t *testing.T) {
		f := newFixture(t)
		f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrBudgetNotFound)
		if _, err := NewGetPayment(f.payments, f.budgets).Execute(context.Background(), p.ID(), owner); !errors.Is(err, domainerrors.ErrBudgetNotFound) {
			t.Fatalf("veio %v", err)
		}
	})
}
