package budgetuc

import (
	"context"
	"errors"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"go.uber.org/mock/gomock"
)

func TestGetBudgetByID(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)

	v, err := NewGetBudgetByID(f.budgets, f.payments).Execute(context.Background(), b.ID(), owner())
	if err != nil || v.Budget != b || v.Payment != p {
		t.Fatalf("view errada: %+v err=%v", v, err)
	}
}

func TestGetBudgetByOS(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetPendingApproval)
	f.budgets.EXPECT().FindByOSID(gomock.Any(), "os-1").Return(b, nil)
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)

	v, err := NewGetBudgetByOS(f.budgets, f.payments).Execute(context.Background(), "os-1", entities.Actor{Role: entities.RoleAdmin})
	if err != nil || v.Budget != b || v.Payment != nil {
		t.Fatalf("view errada: %+v err=%v", v, err)
	}
}

func TestGetBudget_Erros(t *testing.T) {
	boom := errors.New("db fora")

	t.Run("inexistente", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByOSID(gomock.Any(), "x").Return(nil, domainerrors.ErrBudgetNotFound)
		if _, err := NewGetBudgetByOS(f.budgets, f.payments).Execute(context.Background(), "x", owner()); !errors.Is(err, domainerrors.ErrBudgetNotFound) {
			t.Fatalf("veio %v", err)
		}
		f.budgets.EXPECT().FindByID(gomock.Any(), "x").Return(nil, domainerrors.ErrBudgetNotFound)
		if _, err := NewGetBudgetByID(f.budgets, f.payments).Execute(context.Background(), "x", owner()); !errors.Is(err, domainerrors.ErrBudgetNotFound) {
			t.Fatalf("veio %v", err)
		}
	})

	t.Run("cliente de outro documento", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetPendingApproval)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		other := entities.Actor{Role: entities.RoleClient, Document: "11144477735"}
		if _, err := NewGetBudgetByID(f.budgets, f.payments).Execute(context.Background(), b.ID(), other); !errors.Is(err, domainerrors.ErrNotOwner) {
			t.Fatalf("veio %v", err)
		}
	})

	t.Run("falha ao ler pagamento", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetPendingApproval)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, boom)
		if _, err := NewGetBudgetByID(f.budgets, f.payments).Execute(context.Background(), b.ID(), owner()); !errors.Is(err, boom) {
			t.Fatalf("veio %v", err)
		}
	})
}
