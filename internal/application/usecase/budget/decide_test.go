package budgetuc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

func TestApproveBudget(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetPendingApproval)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

	uc := NewApproveBudget(f.uow)
	uc.now = func() time.Time { return fixedNow }
	got, err := uc.Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: owner(), CorrelationID: "c-9"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != entities.BudgetApproved {
		t.Fatalf("status = %s", got.Status())
	}
	ev := f.events.only(t, contracts.EvtBudgetApproved)
	payload := ev.Payload.(contracts.BudgetApproved)
	if payload.BudgetID != b.ID() || !payload.ApprovedAt.Equal(fixedNow) {
		t.Fatalf("payload: %+v", payload)
	}
	if ev.Meta.SagaID != "saga-1" || ev.Meta.OSID != "os-1" || ev.Meta.CorrelationID != "c-9" {
		t.Fatalf("meta: %+v", ev.Meta)
	}
}

func TestRejectBudget(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetPendingApproval)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)

	got, err := NewRejectBudget(f.uow).Execute(context.Background(),
		ports.DecideBudgetInput{BudgetID: b.ID(), Actor: owner(), Reason: "vou pensar"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status() != entities.BudgetRejected {
		t.Fatalf("status = %s", got.Status())
	}
	payload := f.events.only(t, contracts.EvtBudgetRejected).Payload.(contracts.BudgetRejected)
	if payload.Reason != "vou pensar" || payload.BudgetID != b.ID() {
		t.Fatalf("payload: %+v", payload)
	}
}

type decider interface {
	Execute(context.Context, ports.DecideBudgetInput) (*entities.Budget, error)
}

func TestDecisao_Erros(t *testing.T) {
	boom := errors.New("db fora")
	ucs := map[string]func(f *fixture) decider{
		"approve": func(f *fixture) decider { return NewApproveBudget(f.uow) },
		"reject":  func(f *fixture) decider { return NewRejectBudget(f.uow) },
	}
	for name, build := range ucs {
		t.Run(name+" outro cliente", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetPendingApproval)
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			other := entities.Actor{Role: entities.RoleClient, Document: "11144477735"}
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: other, Reason: "x"})
			if !errors.Is(err, domainerrors.ErrNotOwner) {
				t.Fatalf("esperava ErrNotOwner, veio %v", err)
			}
		})
		t.Run(name+" admin não decide pelo cliente", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetPendingApproval)
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: entities.Actor{Role: entities.RoleAdmin}, Reason: "x"})
			if !errors.Is(err, domainerrors.ErrNotOwner) {
				t.Fatalf("esperava ErrNotOwner, veio %v", err)
			}
		})
		t.Run(name+" transição inválida", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetCancelled)
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: owner(), Reason: "x"})
			if !errors.Is(err, domainerrors.ErrInvalidTransition) || len(f.events.events) != 0 {
				t.Fatalf("esperava ErrInvalidTransition, veio %v", err)
			}
		})
		t.Run(name+" inexistente", func(t *testing.T) {
			f := newFixture(t)
			f.budgets.EXPECT().FindByID(gomock.Any(), "x").Return(nil, domainerrors.ErrBudgetNotFound)
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: "x", Actor: owner(), Reason: "x"})
			if !errors.Is(err, domainerrors.ErrBudgetNotFound) {
				t.Fatalf("veio %v", err)
			}
		})
		t.Run(name+" conflito de versão", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetPendingApproval)
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.budgets.EXPECT().Update(gomock.Any(), b).Return(domainerrors.ErrConcurrentUpdate)
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: owner(), Reason: "x"})
			if !errors.Is(err, domainerrors.ErrConcurrentUpdate) || len(f.events.events) != 0 {
				t.Fatalf("veio %v", err)
			}
		})
		t.Run(name+" falha no outbox desfaz", func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetPendingApproval)
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.budgets.EXPECT().Update(gomock.Any(), b).Return(nil)
			f.events.err = boom
			_, err := build(f).Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: owner(), Reason: "x"})
			if !errors.Is(err, boom) {
				t.Fatalf("veio %v", err)
			}
		})
	}
}
