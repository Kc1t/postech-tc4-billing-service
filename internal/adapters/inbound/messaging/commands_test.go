package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports/mocks"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	pkgmessaging "github.com/Kc1t/postech-tc4-billing-service/pkg/messaging"
	"go.uber.org/mock/gomock"
)

type handlers struct {
	h       *CommandHandlers
	inbox   *mocks.MockInbox
	create  *mocks.MockCreateBudgetUseCase
	request *mocks.MockRequestPaymentUseCase
	cancel  *mocks.MockCancelBudgetUseCase
	refund  *mocks.MockRefundPaymentUseCase
}

func newHandlers(t *testing.T) handlers {
	t.Helper()
	ctrl := gomock.NewController(t)
	m := handlers{
		inbox:   mocks.NewMockInbox(ctrl),
		create:  mocks.NewMockCreateBudgetUseCase(ctrl),
		request: mocks.NewMockRequestPaymentUseCase(ctrl),
		cancel:  mocks.NewMockCancelBudgetUseCase(ctrl),
		refund:  mocks.NewMockRefundPaymentUseCase(ctrl),
	}
	m.h = NewCommandHandlers(m.inbox, m.create, m.request, m.cancel, m.refund)
	return m
}

// passThroughInbox executa fn (primeira entrega).
func passThroughInbox(m handlers) {
	m.inbox.EXPECT().Once(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ contracts.Envelope, fn func(context.Context) error) (bool, error) {
			return false, fn(ctx)
		})
}

func envelope(t *testing.T, msgType string, payload any) contracts.Envelope {
	t.Helper()
	env, err := contracts.NewEnvelope(msgType, "os-service",
		contracts.Meta{SagaID: "saga-1", OSID: "os-1", CorrelationID: "corr-1"}, payload)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestCreateBudget_TraduzOComando(t *testing.T) {
	m := newHandlers(t)
	passThroughInbox(m)
	env := envelope(t, contracts.CmdCreateBudget, contracts.CreateBudget{
		OSCode:          12,
		Requester:       contracts.Requester{ID: "r1", Name: "Ana", Email: "a@x.com", Document: "52998224725"},
		Items:           []contracts.Item{{Kind: contracts.ItemKindPart, Ref: "P1", Description: "Peça", Quantity: 2, UnitPrice: 9.9}},
		Total:           19.8,
		SimulateFailure: contracts.FailBudget,
	})
	m.create.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, cmd ports.CreateBudgetCommand) error {
		want := contracts.Meta{SagaID: "saga-1", OSID: "os-1", CorrelationID: "corr-1", CausationID: env.MessageID}
		if cmd.Meta != want || cmd.OSCode != 12 || cmd.Total != 19.8 || cmd.SimulateFailure != "budget" ||
			cmd.Requester.Document != "52998224725" || len(cmd.Items) != 1 ||
			cmd.Items[0] != (entities.BudgetItem{Kind: entities.ItemPart, Ref: "P1", Description: "Peça", Quantity: 2, UnitPrice: 9.9}) {
			t.Fatalf("comando traduzido errado: %+v", cmd)
		}
		return nil
	})
	if err := m.h.CreateBudget(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

func TestDemaisComandos(t *testing.T) {
	t.Run("RequestPayment", func(t *testing.T) {
		m := newHandlers(t)
		passThroughInbox(m)
		m.request.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, cmd ports.RequestPaymentCommand) error {
			if cmd.BudgetID != "b1" || cmd.SimulateFailure != "payment" || cmd.Meta.SagaID != "saga-1" {
				t.Fatalf("%+v", cmd)
			}
			return nil
		})
		env := envelope(t, contracts.CmdRequestPayment, contracts.RequestPayment{BudgetID: "b1", SimulateFailure: "payment"})
		if err := m.h.RequestPayment(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("CancelBudget", func(t *testing.T) {
		m := newHandlers(t)
		passThroughInbox(m)
		m.cancel.EXPECT().Execute(gomock.Any(), ports.CancelBudgetCommand{
			Meta: contracts.Meta{SagaID: "saga-1", OSID: "os-1", CorrelationID: "corr-1", CausationID: "fixed"}, BudgetID: "b1", Reason: "timeout",
		}).Return(nil)
		env := envelope(t, contracts.CmdCancelBudget, contracts.CancelBudget{BudgetID: "b1", Reason: "timeout"})
		env.MessageID = "fixed"
		if err := m.h.CancelBudget(context.Background(), env); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("RefundPayment", func(t *testing.T) {
		m := newHandlers(t)
		passThroughInbox(m)
		boom := errors.New("provedor fora")
		m.refund.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(boom)
		env := envelope(t, contracts.CmdRefundPayment, contracts.RefundPayment{BudgetID: "b1", Reason: "reparo rejeitado"})
		if err := m.h.RefundPayment(context.Background(), env); !errors.Is(err, boom) {
			t.Fatalf("falha transitória deve voltar ao consumidor: %v", err)
		}
	})
}

func TestDuplicataNaoExecutaCasoDeUso(t *testing.T) {
	m := newHandlers(t)
	m.inbox.EXPECT().Once(gomock.Any(), gomock.Any(), gomock.Any()).Return(true, nil)
	// create.Execute não pode ser chamado.
	env := envelope(t, contracts.CmdCreateBudget, contracts.CreateBudget{OSCode: 1})
	if err := m.h.CreateBudget(context.Background(), env); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadMalformadoVaiParaRetryEDLQ(t *testing.T) {
	m := newHandlers(t)
	bad := envelope(t, contracts.CmdCreateBudget, struct{}{})
	bad.Payload = json.RawMessage(`{"os_code":"não é número"}`)
	for name, handle := range map[string]pkgmessaging.Handler{
		"create":  m.h.CreateBudget,
		"request": m.h.RequestPayment,
		"cancel":  m.h.CancelBudget,
		"refund":  m.h.RefundPayment,
	} {
		b := bad
		b.Payload = json.RawMessage(`[1,2]`)
		if name == "create" {
			b = bad
		}
		if err := handle(context.Background(), b); err == nil {
			t.Errorf("%s: payload malformado deveria devolver erro", name)
		}
	}
}

type registry map[string]pkgmessaging.Handler

func (r registry) Handle(msgType string, h pkgmessaging.Handler) { r[msgType] = h }

func TestRegister(t *testing.T) {
	m := newHandlers(t)
	r := registry{}
	m.h.Register(r)
	for _, typ := range []string{contracts.CmdCreateBudget, contracts.CmdRequestPayment, contracts.CmdCancelBudget, contracts.CmdRefundPayment} {
		if r[typ] == nil {
			t.Errorf("%s não registrado", typ)
		}
	}
	if len(r) != 4 {
		t.Fatalf("registrados: %d", len(r))
	}
}
