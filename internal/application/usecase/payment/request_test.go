package paymentuc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"go.uber.org/mock/gomock"
)

func requestCmd(budgetID string) ports.RequestPaymentCommand {
	return ports.RequestPaymentCommand{Meta: meta, BudgetID: budgetID}
}

func TestRequestPayment_CriaLink(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	var created *entities.Payment
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
	f.gateway.EXPECT().Provider().Return(entities.ProviderMercadoPago)
	f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, p *entities.Payment) error {
		created = p
		return nil
	})
	f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, req ports.CheckoutRequest) (string, string, error) {
		if req.PaymentID != created.ID() || req.BudgetID != b.ID() || req.OSCode != 7 || req.Amount != 151 {
			t.Fatalf("checkout com dados errados: %+v", req)
		}
		return "pref-1", "https://mp/checkout?pref_id=pref-1", nil
	})
	f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

	if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
		t.Fatal(err)
	}
	if created.Status() != entities.PaymentPending || created.ProviderPreferenceID() != "pref-1" || created.Provider() != entities.ProviderMercadoPago {
		t.Fatalf("pagamento: %+v", created.Snapshot())
	}
	ev := f.events.only(t, contracts.EvtPaymentLinkCreated)
	got := ev.Payload.(contracts.PaymentLinkCreated)
	if got.PaymentID != created.ID() || got.CheckoutURL != "https://mp/checkout?pref_id=pref-1" || got.BudgetID != b.ID() || ev.Meta != meta {
		t.Fatalf("evento: %+v", ev)
	}
}

func TestRequestPayment_RegrasVioladasViramPaymentFailed(t *testing.T) {
	t.Run("orçamento inexistente", func(t *testing.T) {
		f := newFixture(t)
		f.budgets.EXPECT().FindByID(gomock.Any(), "x").Return(nil, domainerrors.ErrBudgetNotFound)
		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd("x")); err != nil {
			t.Fatal(err)
		}
		if got := f.events.only(t, contracts.EvtPaymentFailed).Payload.(contracts.PaymentFailed); got.BudgetID != "x" || got.PaymentID != "" {
			t.Fatalf("payload: %+v", got)
		}
	})

	t.Run("orçamento não aprovado", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetPendingApproval)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
		f.gateway.EXPECT().Provider().Return(entities.ProviderFake)
		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		got := f.events.only(t, contracts.EvtPaymentFailed).Payload.(contracts.PaymentFailed)
		if !strings.Contains(got.Reason, "não está aprovado") {
			t.Fatalf("motivo: %q", got.Reason)
		}
	})

	t.Run("provedor recusa definitivamente", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		var created *entities.Payment
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
		f.gateway.EXPECT().Provider().Return(entities.ProviderMercadoPago)
		f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, p *entities.Payment) error { created = p; return nil })
		f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).Return("", "", fmt.Errorf("%w: status 400: invalid unit_price", ports.ErrGatewayRejected))
		f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if created.Status() != entities.PaymentFailed {
			t.Fatalf("status = %s", created.Status())
		}
		got := f.events.only(t, contracts.EvtPaymentFailed).Payload.(contracts.PaymentFailed)
		if got.PaymentID != created.ID() || !strings.Contains(got.Reason, "invalid unit_price") {
			t.Fatalf("payload: %+v", got)
		}
	})
}

func TestRequestPayment_FalhaSimulada(t *testing.T) {
	cmd := requestCmd("")
	cmd.SimulateFailure = contracts.FailPayment

	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	cmd.BudgetID = b.ID()
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
	f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
	f.gateway.EXPECT().Provider().Return(entities.ProviderFake)
	f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	// CreateCheckout não pode ser chamado.

	if err := NewRequestPayment(f.uow, f.gateway, true).Execute(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	got := f.events.only(t, contracts.EvtPaymentFailed).Payload.(contracts.PaymentFailed)
	if !strings.Contains(got.Reason, "simulada") || got.PaymentID == "" {
		t.Fatalf("payload: %+v", got)
	}
}

func TestRequestPayment_Idempotente(t *testing.T) {
	t.Run("cobrança em aberto: reenvia o link", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if got := f.events.only(t, contracts.EvtPaymentLinkCreated).Payload.(contracts.PaymentLinkCreated); got.PaymentID != p.ID() {
			t.Fatalf("payload: %+v", got)
		}
	})

	t.Run("já pago: reenvia a aprovação", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentApproved, "mp-1")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(p, nil)
		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if got := f.events.only(t, contracts.EvtPaymentApproved).Payload.(contracts.PaymentApproved); got.ProviderPaymentID != "mp-1" {
			t.Fatalf("payload: %+v", got)
		}
	})

	t.Run("tentativa anterior falhou: cria outra", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		old := newPayment(t, b, entities.PaymentFailed, "")
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(old, nil)
		f.gateway.EXPECT().Provider().Return(entities.ProviderFake)
		f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
		f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).Return("p", "http://x/1", nil)
		f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
		if err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID())); err != nil {
			t.Fatal(err)
		}
		if got := f.events.only(t, contracts.EvtPaymentLinkCreated).Payload.(contracts.PaymentLinkCreated); got.PaymentID == old.ID() {
			t.Fatal("deveria ser uma cobrança nova")
		}
	})
}

func TestRequestPayment_ErrosTransitorios(t *testing.T) {
	boom := errors.New("db fora")
	type setup func(f *fixture, b *entities.Budget)
	base := func(f *fixture, b *entities.Budget) {
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
		f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, domainerrors.ErrPaymentNotFound)
		f.gateway.EXPECT().Provider().Return(entities.ProviderFake)
	}
	cases := map[string]struct {
		setup setup
		want  error
	}{
		"ler orçamento": {func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(nil, boom)
		}, boom},
		"ler pagamento": {func(f *fixture, b *entities.Budget) {
			f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
			f.payments.EXPECT().FindLatestByBudgetID(gomock.Any(), b.ID()).Return(nil, boom)
		}, boom},
		"criar pagamento": {func(f *fixture, b *entities.Budget) {
			base(f, b)
			f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(boom)
		}, boom},
		"provedor indisponível": {func(f *fixture, b *entities.Budget) {
			base(f, b)
			f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).Return("", "", ports.ErrGatewayUnavailable)
		}, ports.ErrGatewayUnavailable},
		"link vazio do provedor": {func(f *fixture, b *entities.Budget) {
			base(f, b)
			f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).Return("p", "", nil)
		}, domainerrors.ErrInvalidInput},
		"salvar link": {func(f *fixture, b *entities.Budget) {
			base(f, b)
			f.payments.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
			f.gateway.EXPECT().CreateCheckout(gomock.Any(), gomock.Any()).Return("p", "http://x", nil)
			f.payments.EXPECT().Update(gomock.Any(), gomock.Any()).Return(domainerrors.ErrConcurrentUpdate)
		}, domainerrors.ErrConcurrentUpdate},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetApproved)
			tc.setup(f, b)
			err := NewRequestPayment(f.uow, f.gateway, false).Execute(context.Background(), requestCmd(b.ID()))
			if !errors.Is(err, tc.want) {
				t.Fatalf("esperava %v, veio %v", tc.want, err)
			}
			f.events.none(t)
		})
	}
}
