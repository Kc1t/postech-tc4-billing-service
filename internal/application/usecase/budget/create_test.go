package budgetuc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

func createCmd() ports.CreateBudgetCommand {
	return ports.CreateBudgetCommand{
		Meta:      meta,
		OSCode:    7,
		Requester: entities.Requester{ID: "r1", Name: "Ana", Email: "ana@example.com", Document: "529.982.247-25"},
		Items:     items(),
		Total:     151,
	}
}

func TestCreateBudget_CriaEPublicaBudgetCreated(t *testing.T) {
	f := newFixture(t)
	var saved *entities.Budget
	f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(nil, domainerrors.ErrBudgetNotFound)
	f.budgets.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, b *entities.Budget) error {
		saved = b
		return nil
	})

	uc := NewCreateBudget(f.uow, false)
	if err := uc.Execute(context.Background(), createCmd()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if saved == nil || saved.Status() != entities.BudgetPendingApproval || saved.OSID() != "os-1" || saved.SagaID() != "saga-1" {
		t.Fatalf("orçamento salvo errado: %+v", saved)
	}
	ev := f.events.only(t, contracts.EvtBudgetCreated)
	got := ev.Payload.(contracts.BudgetCreated)
	if got.BudgetID != saved.ID() || got.Total != 151 || ev.Meta != meta {
		t.Fatalf("evento errado: %+v", ev)
	}
}

func TestCreateBudget_IdempotentePorOS(t *testing.T) {
	f := newFixture(t)
	existing := newBudget(t, entities.BudgetPendingApproval)
	f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(existing, nil)
	// Create não pode ser chamado.

	if err := NewCreateBudget(f.uow, false).Execute(context.Background(), createCmd()); err != nil {
		t.Fatal(err)
	}
	if got := f.events.only(t, contracts.EvtBudgetCreated).Payload.(contracts.BudgetCreated); got.BudgetID != existing.ID() {
		t.Fatalf("deveria responder com o orçamento existente: %+v", got)
	}
}

func TestCreateBudget_RegraVioladaViraEventoDeFalha(t *testing.T) {
	f := newFixture(t)
	f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(nil, domainerrors.ErrBudgetNotFound)
	cmd := createCmd()
	cmd.Total = 999

	if err := NewCreateBudget(f.uow, false).Execute(context.Background(), cmd); err != nil {
		t.Fatalf("regra de negócio não pode virar erro do handler: %v", err)
	}
	got := f.events.only(t, contracts.EvtBudgetCreationFailed).Payload.(contracts.BudgetCreationFailed)
	if !strings.Contains(got.Reason, "total informado") {
		t.Fatalf("motivo deveria explicar a falha: %q", got.Reason)
	}
}

func TestCreateBudget_FalhaSimulada(t *testing.T) {
	cmd := createCmd()
	cmd.SimulateFailure = contracts.FailBudget

	t.Run("flag ligada não cria e publica falha", func(t *testing.T) {
		f := newFixture(t)
		if err := NewCreateBudget(f.uow, true).Execute(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		got := f.events.only(t, contracts.EvtBudgetCreationFailed).Payload.(contracts.BudgetCreationFailed)
		if !strings.Contains(got.Reason, "simulada") {
			t.Fatalf("motivo: %q", got.Reason)
		}
	})

	t.Run("flag desligada ignora o campo", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(nil, domainerrors.ErrBudgetNotFound)
		f.budgets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
		if err := NewCreateBudget(f.uow, false).Execute(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		f.events.only(t, contracts.EvtBudgetCreated)
	})
}

func TestCreateBudget_ErrosTransitoriosVoltamParaAFila(t *testing.T) {
	boom := errors.New("conexão perdida")

	t.Run("leitura", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(nil, boom)
		if err := NewCreateBudget(f.uow, false).Execute(context.Background(), createCmd()); !errors.Is(err, boom) {
			t.Fatalf("esperava o erro, veio %v", err)
		}
	})

	t.Run("corrida na unicidade de os_id", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(nil, domainerrors.ErrBudgetNotFound)
		f.budgets.EXPECT().Create(gomock.Any(), gomock.Any()).Return(domainerrors.ErrBudgetAlreadyExists)
		err := NewCreateBudget(f.uow, false).Execute(context.Background(), createCmd())
		if !errors.Is(err, domainerrors.ErrBudgetAlreadyExists) || len(f.events.events) != 0 {
			t.Fatalf("esperava erro sem eventos, veio %v / %+v", err, f.events.events)
		}
	})
}
