package budgetuc

import (
	"context"
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports/mocks"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

var (
	fixedNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	meta     = contracts.Meta{SagaID: "saga-1", OSID: "os-1", CorrelationID: "corr-1", CausationID: "msg-0"}
	ownerDoc = "52998224725"
)

// published é um evento registrado pelo outbox falso.
type published struct {
	Type    string
	Meta    contracts.Meta
	Payload any
}

// eventRecorder substitui o outbox: guarda os eventos para as asserções.
type eventRecorder struct {
	events []published
	err    error
}

func (r *eventRecorder) Publish(_ context.Context, msgType string, m contracts.Meta, payload any) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, published{Type: msgType, Meta: m, Payload: payload})
	return nil
}

func (r *eventRecorder) only(t *testing.T, msgType string) published {
	t.Helper()
	if len(r.events) != 1 || r.events[0].Type != msgType {
		t.Fatalf("esperava só %s, publicados: %+v", msgType, r.events)
	}
	return r.events[0]
}

// fixture liga UnitOfWork → Tx → repositórios mockados, como o adapter Postgres faz.
type fixture struct {
	uow      *mocks.MockUnitOfWork
	budgets  *mocks.MockBudgetRepository
	payments *mocks.MockPaymentRepository
	gateway  *mocks.MockPaymentGateway
	events   *eventRecorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &fixture{
		uow:      mocks.NewMockUnitOfWork(ctrl),
		budgets:  mocks.NewMockBudgetRepository(ctrl),
		payments: mocks.NewMockPaymentRepository(ctrl),
		gateway:  mocks.NewMockPaymentGateway(ctrl),
		events:   &eventRecorder{},
	}
	tx := mocks.NewMockTx(ctrl)
	tx.EXPECT().Budgets().Return(f.budgets).AnyTimes()
	tx.EXPECT().Payments().Return(f.payments).AnyTimes()
	tx.EXPECT().Events().Return(f.events).AnyTimes()
	f.uow.EXPECT().Do(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(context.Context, ports.Tx) error) error { return fn(ctx, tx) }).
		AnyTimes()
	return f
}

func items() []entities.BudgetItem {
	return []entities.BudgetItem{
		{Kind: entities.ItemService, Ref: "10", Description: "Alinhamento", Quantity: 1, UnitPrice: 80},
		{Kind: entities.ItemPart, Ref: "PST-1", Description: "Pastilha", Quantity: 2, UnitPrice: 35.5},
	}
}

func newBudget(t *testing.T, status entities.BudgetStatus) *entities.Budget {
	t.Helper()
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 7, SagaID: "saga-1",
		Requester: entities.Requester{ID: "r1", Name: "Ana", Email: "ana@example.com", Document: ownerDoc},
		Items:     items(), Total: 151,
	}, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	s := b.Snapshot()
	s.Status = status
	return entities.RestoreBudget(s)
}

func newPayment(t *testing.T, b *entities.Budget, status entities.PaymentStatus, providerID string) *entities.Payment {
	t.Helper()
	approved := newBudget(t, entities.BudgetApproved)
	p, err := entities.NewPayment(approved, entities.ProviderFake, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Snapshot()
	s.BudgetID = b.ID()
	s.Status = status
	s.ProviderPaymentID = providerID
	return entities.RestorePayment(s)
}

func owner() entities.Actor {
	return entities.Actor{UserID: "r1", Role: entities.RoleClient, Document: ownerDoc}
}
