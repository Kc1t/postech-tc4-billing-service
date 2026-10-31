package postgresql

// Testes de contrato das mensagens do billing-service, com os casos de uso reais, este adapter
// (UnitOfWork → outbox, Inbox) e o Postgres de teste:
//   - produtor: todo evento que os casos de uso gravam no outbox passa no JSON Schema do seu tipo
//     (pkg/contracts/schemas);
//   - consumidor: o exemplo publicado no schema de cada comando da fila billing-commands,
//     entregue ao handler registrado no consumidor, é aceito e produz o efeito do contrato.

import (
	"context"
	"slices"
	"testing"

	inboundmsg "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/messaging"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/fakegateway"
	budgetuc "github.com/Kc1t/postech-tc4-billing-service/internal/application/usecase/budget"
	paymentuc "github.com/Kc1t/postech-tc4-billing-service/internal/application/usecase/payment"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	pkgmessaging "github.com/Kc1t/postech-tc4-billing-service/pkg/messaging"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/outbox"
	"github.com/google/uuid"
)

const contractDocument = "52998224725"

// billingApp liga os casos de uso ao UnitOfWork real, como o bootstrap faz (gateway fake, injeção
// de falhas ligada).
type billingApp struct {
	uow      *UnitOfWork
	create   *budgetuc.CreateBudget
	approve  *budgetuc.ApproveBudget
	reject   *budgetuc.RejectBudget
	cancel   *budgetuc.CancelBudget
	request  *paymentuc.RequestPayment
	simulate *paymentuc.SimulatePayment
	refund   *paymentuc.RefundPayment
}

func newBillingApp() billingApp {
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	gw := fakegateway.New("http://localhost:8082")
	return billingApp{
		uow:      uow,
		create:   budgetuc.NewCreateBudget(uow, true),
		approve:  budgetuc.NewApproveBudget(uow),
		reject:   budgetuc.NewRejectBudget(uow),
		cancel:   budgetuc.NewCancelBudget(uow, gw),
		request:  paymentuc.NewRequestPayment(uow, gw, true),
		simulate: paymentuc.NewSimulatePayment(uow, NewPaymentRepository(testDB), gw),
		refund:   paymentuc.NewRefundPayment(uow, gw),
	}
}

// newSaga devolve a identificação de uma saga nova e apaga, no fim do teste, o que ela gerar.
func newSaga(t *testing.T) contracts.Meta {
	t.Helper()
	m := contracts.Meta{SagaID: uuid.NewString(), OSID: uuid.NewString(), CorrelationID: "req-" + uuid.NewString()[:8], CausationID: uuid.NewString()}
	t.Cleanup(func() { cleanOS(m.OSID, "") })
	return m
}

func cleanOS(osID, budgetID string) {
	testDB.Exec("DELETE FROM payments WHERE os_id = ? OR budget_id::text = ?", osID, budgetID)
	testDB.Exec("DELETE FROM budgets WHERE os_id = ? OR id::text = ?", osID, budgetID)
}

func createCommand(m contracts.Meta) ports.CreateBudgetCommand {
	return ports.CreateBudgetCommand{
		Meta: m, OSCode: 1042,
		Requester: entities.Requester{ID: uuid.NewString(), Name: "Ana Souza", Email: "ana@example.com", Document: contractDocument},
		Items: []entities.BudgetItem{
			{Kind: entities.ItemService, Ref: "4012", Description: "Troca de pastilhas", Quantity: 1, UnitPrice: 150},
			{Kind: entities.ItemPart, Ref: "BOSCH-0986BB0606", Description: "Pastilha dianteira", Quantity: 2, UnitPrice: 90},
		},
		Total: 330,
	}
}

func client() entities.Actor {
	return entities.Actor{UserID: uuid.NewString(), Role: entities.RoleClient, Document: contractDocument}
}

func (a billingApp) open(t *testing.T, m contracts.Meta) *entities.Budget {
	t.Helper()
	ctx := context.Background()
	if err := a.create.Execute(ctx, createCommand(m)); err != nil {
		t.Fatal(err)
	}
	b, err := NewBudgetRepository(testDB).FindByOSID(ctx, m.OSID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (a billingApp) approved(t *testing.T, m contracts.Meta) *entities.Budget {
	t.Helper()
	b := a.open(t, m)
	if _, err := a.approve.Execute(context.Background(), ports.DecideBudgetInput{BudgetID: b.ID(), Actor: client(), CorrelationID: "req-aprova"}); err != nil {
		t.Fatal(err)
	}
	return b
}

func latestPayment(t *testing.T, budgetID string) *entities.Payment {
	t.Helper()
	p, err := NewPaymentRepository(testDB).FindLatestByBudgetID(context.Background(), budgetID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestContrato_ProdutorOutboxSegueOSchema(t *testing.T) {
	ctx := context.Background()
	a := newBillingApp()
	var sagas []string
	saga := func() contracts.Meta {
		m := newSaga(t)
		sagas = append(sagas, m.SagaID)
		return m
	}
	ok := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// Orçamento aprovado, pago pelo simulador do gateway fake e estornado.
	m := saga()
	b := a.approved(t, m)
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: b.ID()}))
	p := latestPayment(t, b.ID())
	_, err := a.simulate.Execute(ctx, ports.SimulatePaymentInput{PaymentID: p.ID(), Status: "approved", CorrelationID: "req-webhook"})
	ok(err)
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: b.ID()})) // reentrega: PaymentApproved de novo
	ok(a.refund.Execute(ctx, ports.RefundPaymentCommand{Meta: m, BudgetID: b.ID(), Reason: "execução recusada"}))

	// Recusa do cliente, cancelamento (compensação) e falhas.
	m = saga()
	b = a.open(t, m)
	_, err = a.reject.Execute(ctx, ports.DecideBudgetInput{BudgetID: b.ID(), Actor: client(), Reason: "caro demais", CorrelationID: "req-recusa"})
	ok(err)

	m = saga()
	b = a.approved(t, m)
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: b.ID()}))
	ok(a.cancel.Execute(ctx, ports.CancelBudgetCommand{Meta: m, BudgetID: b.ID(), Reason: "cancelada pela oficina"}))

	m = saga()
	cmd := createCommand(m)
	cmd.SimulateFailure = contracts.FailBudget
	ok(a.create.Execute(ctx, cmd))
	m = saga()
	cmd = createCommand(m)
	cmd.Total = 1 // não bate com a soma dos itens
	ok(a.create.Execute(ctx, cmd))

	m = saga()
	b = a.approved(t, m)
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: b.ID(), SimulateFailure: contracts.FailPayment}))
	m = saga()
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: uuid.NewString()})) // orçamento inexistente: payment_id vazio
	m = saga()
	b = a.approved(t, m)
	ok(a.request.Execute(ctx, ports.RequestPaymentCommand{Meta: m, BudgetID: b.ID()}))
	_, err = a.simulate.Execute(ctx, ports.SimulatePaymentInput{PaymentID: latestPayment(t, b.ID()).ID(), Status: "rejected"})
	ok(err)

	seen := map[string]int{}
	for _, s := range sagas {
		msgs := outboxFor(t, s)
		if len(msgs) == 0 {
			t.Fatalf("saga %s sem eventos no outbox", s)
		}
		for _, msg := range msgs {
			if err := contracts.Validate([]byte(msg.Body)); err != nil {
				t.Errorf("%s gravado no outbox fora do contrato: %v\n%s", msg.Type, err, msg.Body)
			}
			seen[msg.Type]++
		}
	}
	want := []string{
		contracts.EvtBudgetCreated, contracts.EvtBudgetCreationFailed, contracts.EvtBudgetApproved,
		contracts.EvtBudgetRejected, contracts.EvtBudgetCancelled, contracts.EvtPaymentLinkCreated,
		contracts.EvtPaymentApproved, contracts.EvtPaymentFailed, contracts.EvtPaymentRefunded,
	}
	for _, typ := range want {
		if seen[typ] == 0 {
			t.Errorf("nenhum %s produzido: o teste precisa cobrir todos os eventos do billing-service", typ)
		}
	}
	for typ := range seen {
		if !slices.Contains(want, typ) {
			t.Errorf("o billing-service produziu %s, fora da lista de eventos que ele publica", typ)
		}
	}
	t.Logf("eventos do outbox validados por tipo: %v", seen)
}

type registry map[string]pkgmessaging.Handler

func (r registry) Handle(msgType string, h pkgmessaging.Handler) { r[msgType] = h }

// example devolve o primeiro exemplo do schema do tipo, já lido como o consumidor lê.
func example(t *testing.T, msgType string) contracts.Envelope {
	t.Helper()
	examples, err := contracts.Examples(msgType)
	if err != nil {
		t.Fatal(err)
	}
	env, err := contracts.Parse(examples[0])
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func examplePayload[T any](t *testing.T, msgType string) T {
	t.Helper()
	v, err := contracts.Decode[T](example(t, msgType))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// seedExampleBudget grava o orçamento citado pelos exemplos (mesmos budget_id, os_id e saga_id),
// no status pedido, com os itens do exemplo do CreateBudget.
func seedExampleBudget(t *testing.T, env contracts.Envelope, budgetID string, status entities.BudgetStatus) *entities.Budget {
	t.Helper()
	cb := examplePayload[contracts.CreateBudget](t, contracts.CmdCreateBudget)
	items := make([]entities.BudgetItem, len(cb.Items))
	for i, it := range cb.Items {
		items[i] = entities.BudgetItem{Kind: entities.ItemKind(it.Kind), Ref: it.Ref, Description: it.Description, Quantity: it.Quantity, UnitPrice: it.UnitPrice}
	}
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: env.OSID, OSCode: cb.OSCode, SagaID: env.SagaID, Items: items, Total: cb.Total,
		Requester: entities.Requester{ID: cb.Requester.ID, Name: cb.Requester.Name, Email: cb.Requester.Email, Document: cb.Requester.Document},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	s := b.Snapshot()
	s.ID, s.Status = budgetID, status
	if status == entities.BudgetApproved {
		s.DecidedAt = &now
	}
	b = entities.RestoreBudget(s)
	if err := NewBudgetRepository(testDB).Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContrato_ConsumidorAceitaOsExemplosDosComandos(t *testing.T) {
	ctx := context.Background()
	a := newBillingApp()
	consumer := "billing-commands-contrato-" + uuid.NewString()[:8]
	t.Cleanup(func() { testDB.Exec("DELETE FROM processed_messages WHERE consumer = ?", consumer) })
	reg := registry{}
	inboundmsg.NewCommandHandlers(NewInbox(testDB, consumer), a.create, a.request, a.cancel, a.refund).Register(reg)

	// deliver entrega o exemplo do tipo ao handler registrado e devolve o único evento gravado no
	// outbox em resposta, já validado contra o schema.
	deliver := func(t *testing.T, msgType string, seed func(env contracts.Envelope)) (contracts.Envelope, contracts.Envelope) {
		t.Helper()
		env := example(t, msgType)
		ref, _ := contracts.Decode[struct {
			BudgetID string `json:"budget_id"`
		}](env)
		clean := func() {
			cleanOS(env.OSID, ref.BudgetID)
			testDB.Exec("DELETE FROM outbox_messages WHERE saga_id = ?", env.SagaID)
		}
		clean()
		t.Cleanup(clean)
		if seed != nil {
			seed(env)
		}
		h, ok := reg[msgType]
		if !ok {
			t.Fatalf("nenhum handler registrado para %s", msgType)
		}
		if err := h(ctx, env); err != nil {
			t.Fatalf("o exemplo de %s foi recusado: %v", msgType, err)
		}
		var msgs []outbox.Message
		if err := testDB.Where("saga_id = ?", env.SagaID).Find(&msgs).Error; err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 {
			t.Fatalf("esperava 1 evento em resposta, veio %d", len(msgs))
		}
		if err := contracts.Validate([]byte(msgs[0].Body)); err != nil {
			t.Fatalf("resposta fora do contrato: %v", err)
		}
		reply, err := contracts.Parse([]byte(msgs[0].Body))
		if err != nil {
			t.Fatal(err)
		}
		if reply.CausationID != env.MessageID || reply.OSID != env.OSID || reply.CorrelationID != env.CorrelationID {
			t.Fatalf("resposta sem a identificação do comando: %+v", reply)
		}
		return env, reply
	}
	covered := map[string]bool{}

	t.Run(contracts.CmdCreateBudget, func(t *testing.T) {
		covered[contracts.CmdCreateBudget] = true
		env, reply := deliver(t, contracts.CmdCreateBudget, nil)
		cmd := examplePayload[contracts.CreateBudget](t, contracts.CmdCreateBudget)
		b, err := NewBudgetRepository(testDB).FindByOSID(ctx, env.OSID)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status() != entities.BudgetPendingApproval || b.Total() != cmd.Total || len(b.Items()) != len(cmd.Items) ||
			b.SagaID() != env.SagaID || b.Requester().Document != cmd.Requester.Document {
			t.Fatalf("orçamento criado do exemplo: %+v", b.Snapshot())
		}
		got, _ := contracts.Decode[contracts.BudgetCreated](reply)
		if reply.Type != contracts.EvtBudgetCreated || got.BudgetID != b.ID() || got.Total != cmd.Total {
			t.Fatalf("resposta: %s %+v", reply.Type, got)
		}
	})

	t.Run(contracts.CmdRequestPayment, func(t *testing.T) {
		covered[contracts.CmdRequestPayment] = true
		cmd := examplePayload[contracts.RequestPayment](t, contracts.CmdRequestPayment)
		_, reply := deliver(t, contracts.CmdRequestPayment, func(env contracts.Envelope) {
			seedExampleBudget(t, env, cmd.BudgetID, entities.BudgetApproved)
		})
		p := latestPayment(t, cmd.BudgetID)
		got, _ := contracts.Decode[contracts.PaymentLinkCreated](reply)
		if p.Status() != entities.PaymentPending || reply.Type != contracts.EvtPaymentLinkCreated ||
			got.PaymentID != p.ID() || got.CheckoutURL != p.CheckoutURL() {
			t.Fatalf("cobrança do exemplo: %s %+v / %s %+v", p.Status(), p.Snapshot(), reply.Type, got)
		}
	})

	t.Run(contracts.CmdCancelBudget, func(t *testing.T) {
		covered[contracts.CmdCancelBudget] = true
		cmd := examplePayload[contracts.CancelBudget](t, contracts.CmdCancelBudget)
		_, reply := deliver(t, contracts.CmdCancelBudget, func(env contracts.Envelope) {
			seedExampleBudget(t, env, cmd.BudgetID, entities.BudgetPendingApproval)
		})
		b, err := NewBudgetRepository(testDB).FindByID(ctx, cmd.BudgetID)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status() != entities.BudgetCancelled || b.Reason() != cmd.Reason || reply.Type != contracts.EvtBudgetCancelled {
			t.Fatalf("cancelamento do exemplo: %s %q / %s", b.Status(), b.Reason(), reply.Type)
		}
	})

	t.Run(contracts.CmdRefundPayment, func(t *testing.T) {
		covered[contracts.CmdRefundPayment] = true
		cmd := examplePayload[contracts.RefundPayment](t, contracts.CmdRefundPayment)
		_, reply := deliver(t, contracts.CmdRefundPayment, func(env contracts.Envelope) {
			b := seedExampleBudget(t, env, cmd.BudgetID, entities.BudgetApproved)
			p, err := entities.NewPayment(b, entities.ProviderFake, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.LinkCreated("fake-pref-"+p.ID(), "http://localhost:8082/fake-checkout/"+p.ID(), now); err != nil {
				t.Fatal(err)
			}
			if err := p.Approve("fake-"+p.ID(), p.Amount(), now); err != nil {
				t.Fatal(err)
			}
			if err := NewPaymentRepository(testDB).Create(ctx, p); err != nil {
				t.Fatal(err)
			}
		})
		p := latestPayment(t, cmd.BudgetID)
		got, _ := contracts.Decode[contracts.PaymentRefunded](reply)
		if p.Status() != entities.PaymentRefunded || reply.Type != contracts.EvtPaymentRefunded || got.PaymentID != p.ID() {
			t.Fatalf("estorno do exemplo: %s / %s %+v", p.Status(), reply.Type, got)
		}
	})

	for typ := range reg {
		if !covered[typ] {
			t.Errorf("o billing-service consome %s, mas o teste não entregou o exemplo dele", typ)
		}
	}
}
