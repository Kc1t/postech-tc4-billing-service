package paymentuc

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

func notify(id string) ports.PaymentNotificationInput {
	return ports.PaymentNotificationInput{ProviderPaymentID: id, CorrelationID: "mp-req-1"}
}

// expectLoad prepara a releitura do pagamento e do orçamento dentro da transação.
func expectLoad(f *fixture, b *entities.Budget, p *entities.Payment) {
	f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
	f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(b, nil)
}

func provider(id string, status ports.ProviderStatus, p *entities.Payment) ports.ProviderPayment {
	return ports.ProviderPayment{ID: id, Status: status, Amount: p.Amount(), ExternalReference: p.ID()}
}

func TestNotification_Aprovado(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	f.gateway.EXPECT().GetPayment(gomock.Any(), "123").Return(provider("123", ports.ProviderStatusApproved, p), nil)
	expectLoad(f, b, p)
	f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

	if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("123")); err != nil {
		t.Fatal(err)
	}
	if p.Status() != entities.PaymentApproved || p.ProviderPaymentID() != "123" {
		t.Fatalf("pagamento: %+v", p.Snapshot())
	}
	ev := f.events.only(t, contracts.EvtPaymentApproved)
	got := ev.Payload.(contracts.PaymentApproved)
	if got.ProviderPaymentID != "123" || got.Amount != 151 || ev.Meta.SagaID != "saga-1" || ev.Meta.OSID != "os-1" || ev.Meta.CorrelationID != "mp-req-1" {
		t.Fatalf("evento: %+v", ev)
	}
}

func TestNotification_ReentregaNaoDuplicaEvento(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentApproved, "123")
	f.gateway.EXPECT().GetPayment(gomock.Any(), "123").Return(provider("123", ports.ProviderStatusApproved, p), nil)
	expectLoad(f, b, p)

	if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("123")); err != nil {
		t.Fatal(err)
	}
	f.events.none(t)
}

func TestNotification_RecusadoOuCancelado(t *testing.T) {
	for _, st := range []ports.ProviderStatus{ports.ProviderStatusRejected, ports.ProviderStatusCancelled} {
		t.Run(string(st), func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetApproved)
			p := newPayment(t, b, entities.PaymentPending, "")
			r := provider("55", st, p)
			r.StatusDetail = "cc_rejected_other_reason"
			f.gateway.EXPECT().GetPayment(gomock.Any(), "55").Return(r, nil)
			expectLoad(f, b, p)
			f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

			if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("55")); err != nil {
				t.Fatal(err)
			}
			got := f.events.only(t, contracts.EvtPaymentFailed).Payload.(contracts.PaymentFailed)
			if p.Status() != entities.PaymentFailed || !strings.Contains(got.Reason, string(st)) || !strings.Contains(got.Reason, "cc_rejected_other_reason") {
				t.Fatalf("status=%s motivo=%q", p.Status(), got.Reason)
			}
		})
	}

	t.Run("recusa tardia de cobrança já encerrada é ignorada", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentFailed, "55")
		f.gateway.EXPECT().GetPayment(gomock.Any(), "56").Return(provider("56", ports.ProviderStatusRejected, p), nil)
		expectLoad(f, b, p)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("56")); err != nil {
			t.Fatal(err)
		}
		f.events.none(t)
	})
}

func TestNotification_EmAnaliseGuardaID(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentPending, "")
	f.gateway.EXPECT().GetPayment(gomock.Any(), "77").Return(provider("77", ports.ProviderStatusInProcess, p), nil)
	expectLoad(f, b, p)
	f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)

	if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("77")); err != nil {
		t.Fatal(err)
	}
	if p.ProviderPaymentID() != "77" || p.Status() != entities.PaymentPending {
		t.Fatalf("pagamento: %+v", p.Snapshot())
	}
	f.events.none(t)

	// Segunda notificação "pending" do mesmo pagamento não escreve de novo.
	f2 := newFixture(t)
	f2.gateway.EXPECT().GetPayment(gomock.Any(), "77").Return(provider("77", ports.ProviderStatusPending, p), nil)
	expectLoad(f2, b, p)
	if err := NewProcessPaymentNotification(f2.uow, f2.gateway).Execute(context.Background(), notify("77")); err != nil {
		t.Fatal(err)
	}
}

func TestNotification_StatusSemEfeito(t *testing.T) {
	f := newFixture(t)
	b := newBudget(t, entities.BudgetApproved)
	p := newPayment(t, b, entities.PaymentApproved, "1")
	f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(provider("1", ports.ProviderStatusChargedBack, p), nil)
	expectLoad(f, b, p)
	if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); err != nil {
		t.Fatal(err)
	}
	f.events.none(t)
}

func TestNotification_PagamentoOrfaoEhEstornado(t *testing.T) {
	cases := map[string]func(t *testing.T, b *entities.Budget) *entities.Payment{
		"cobrança cancelada": func(t *testing.T, b *entities.Budget) *entities.Payment {
			return newPayment(t, b, entities.PaymentCancelled, "")
		},
		"segundo pagamento da mesma preferência": func(t *testing.T, b *entities.Budget) *entities.Payment {
			return newPayment(t, b, entities.PaymentApproved, "outro")
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			b := newBudget(t, entities.BudgetApproved)
			p := build(t, b)
			f.gateway.EXPECT().GetPayment(gomock.Any(), "900").Return(provider("900", ports.ProviderStatusApproved, p), nil)
			expectLoad(f, b, p)
			f.gateway.EXPECT().Refund(gomock.Any(), "900", "orphan-900").Return(nil)

			if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("900")); err != nil {
				t.Fatal(err)
			}
			f.events.none(t)
		})
	}

	t.Run("valor divergente: falha e estorna", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		r := provider("901", ports.ProviderStatusApproved, p)
		r.Amount = 1
		f.gateway.EXPECT().GetPayment(gomock.Any(), "901").Return(r, nil)
		expectLoad(f, b, p)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(nil)
		f.gateway.EXPECT().Refund(gomock.Any(), "901", "orphan-901").Return(nil)

		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("901")); err != nil {
			t.Fatal(err)
		}
		if p.Status() != entities.PaymentFailed {
			t.Fatalf("status = %s", p.Status())
		}
		f.events.only(t, contracts.EvtPaymentFailed)
	})

	t.Run("estorno do órfão falha: provedor reentrega", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentCancelled, "")
		f.gateway.EXPECT().GetPayment(gomock.Any(), "902").Return(provider("902", ports.ProviderStatusApproved, p), nil)
		expectLoad(f, b, p)
		f.gateway.EXPECT().Refund(gomock.Any(), "902", "orphan-902").Return(ports.ErrGatewayUnavailable)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("902")); !errors.Is(err, ports.ErrGatewayUnavailable) {
			t.Fatalf("veio %v", err)
		}
	})
}

func TestNotification_Ignoradas(t *testing.T) {
	t.Run("sem id", func(t *testing.T) {
		f := newFixture(t)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("")); !errors.Is(err, domainerrors.ErrInvalidInput) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("pagamento inexistente no provedor", func(t *testing.T) {
		f := newFixture(t)
		f.gateway.EXPECT().GetPayment(gomock.Any(), "123456").Return(ports.ProviderPayment{}, ports.ErrGatewayNotFound)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("123456")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sem external_reference", func(t *testing.T) {
		f := newFixture(t)
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(ports.ProviderPayment{Status: ports.ProviderStatusApproved}, nil)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("external_reference de outro sistema", func(t *testing.T) {
		f := newFixture(t)
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(ports.ProviderPayment{ID: "1", Status: ports.ProviderStatusApproved, ExternalReference: "pedido-loja"}, nil)
		f.payments.EXPECT().FindByID(gomock.Any(), "pedido-loja").Return(nil, domainerrors.ErrPaymentNotFound)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNotification_ErrosTransitorios(t *testing.T) {
	boom := errors.New("db fora")
	t.Run("provedor fora", func(t *testing.T) {
		f := newFixture(t)
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(ports.ProviderPayment{}, ports.ErrGatewayUnavailable)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); !errors.Is(err, ports.ErrGatewayUnavailable) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("conflito de versão", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(provider("", ports.ProviderStatusApproved, p), nil)
		expectLoad(f, b, p)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(domainerrors.ErrConcurrentUpdate)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); !errors.Is(err, domainerrors.ErrConcurrentUpdate) {
			t.Fatalf("veio %v", err)
		}
		f.events.none(t)
	})
	t.Run("ler orçamento", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(provider("1", ports.ProviderStatusApproved, p), nil)
		f.payments.EXPECT().FindByID(gomock.Any(), p.ID()).Return(p, nil)
		f.budgets.EXPECT().FindByID(gomock.Any(), b.ID()).Return(nil, boom)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); !errors.Is(err, boom) {
			t.Fatalf("veio %v", err)
		}
	})
	t.Run("salvar falha", func(t *testing.T) {
		f := newFixture(t)
		b := newBudget(t, entities.BudgetApproved)
		p := newPayment(t, b, entities.PaymentPending, "")
		f.gateway.EXPECT().GetPayment(gomock.Any(), "1").Return(provider("1", ports.ProviderStatusRejected, p), nil)
		expectLoad(f, b, p)
		f.payments.EXPECT().Update(gomock.Any(), p).Return(boom)
		if err := NewProcessPaymentNotification(f.uow, f.gateway).Execute(context.Background(), notify("1")); !errors.Is(err, boom) {
			t.Fatalf("veio %v", err)
		}
	})
}
