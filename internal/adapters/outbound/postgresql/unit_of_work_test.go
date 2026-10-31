package postgresql

import (
	"context"
	"errors"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/outbox"
)

const topic = "arn:aws:sns:us-east-1:000000000000:billing-events"

func outboxFor(t *testing.T, sagaID string) []outbox.Message {
	t.Helper()
	var msgs []outbox.Message
	if err := testDB.Where("saga_id = ?", sagaID).Order("created_at").Find(&msgs).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testDB.Exec("DELETE FROM outbox_messages WHERE saga_id = ?", sagaID) })
	return msgs
}

func TestUnitOfWork_CommitGravaEstadoEOutboxJuntos(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	b := seedBudget(t, false)

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		got, err := tx.Budgets().FindByID(ctx, b.ID())
		if err != nil {
			return err
		}
		if err := got.Approve(now); err != nil {
			return err
		}
		if err := tx.Budgets().Update(ctx, got); err != nil {
			return err
		}
		return tx.Events().Publish(ctx, contracts.EvtBudgetApproved,
			contracts.Meta{SagaID: b.SagaID(), OSID: b.OSID(), CorrelationID: "c-1"},
			contracts.BudgetApproved{BudgetID: b.ID(), ApprovedAt: now})
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := outboxFor(t, b.SagaID())
	if len(msgs) != 1 || msgs[0].Type != contracts.EvtBudgetApproved || msgs[0].Destination != topic || msgs[0].DestinationKind != "topic" {
		t.Fatalf("outbox: %+v", msgs)
	}
	env, err := contracts.Parse([]byte(msgs[0].Body))
	if err != nil || env.Producer != "billing-service" || env.OSID != b.OSID() || env.CorrelationID != "c-1" {
		t.Fatalf("envelope: %+v err=%v", env, err)
	}
	reloaded, _ := NewBudgetRepository(testDB).FindByID(ctx, b.ID())
	if reloaded.Status() != entities.BudgetApproved {
		t.Fatalf("estado não gravado: %s", reloaded.Status())
	}
}

func TestUnitOfWork_RollbackDescartaEstadoEOutbox(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	b := seedBudget(t, false)
	boom := errors.New("regra falhou depois de gravar")

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		got, _ := tx.Budgets().FindByID(ctx, b.ID())
		_ = got.Approve(now)
		if err := tx.Budgets().Update(ctx, got); err != nil {
			return err
		}
		if err := tx.Events().Publish(ctx, contracts.EvtBudgetApproved, contracts.Meta{SagaID: b.SagaID()}, struct{}{}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("veio %v", err)
	}
	if msgs := outboxFor(t, b.SagaID()); len(msgs) != 0 {
		t.Fatalf("evento de transação desfeita ficou no outbox: %+v", msgs)
	}
	reloaded, _ := NewBudgetRepository(testDB).FindByID(ctx, b.ID())
	if reloaded.Status() != entities.BudgetPendingApproval {
		t.Fatal("estado de transação desfeita ficou gravado")
	}
}

func TestUnitOfWork_DoAninhadoParticipaDaMesmaTransacao(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	b := seedBudget(t, true)
	boom := errors.New("falha no fim")

	err := uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
		p, err := entities.NewPayment(b, entities.ProviderFake, now)
		if err != nil {
			return err
		}
		if err := uow.Do(ctx, func(ctx context.Context, inner ports.Tx) error {
			return inner.Payments().Create(ctx, p)
		}); err != nil {
			return err
		}
		// A escrita interna é visível na transação externa...
		if _, err := tx.Payments().FindByID(ctx, p.ID()); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("veio %v", err)
	}
	// ...e é desfeita junto com ela.
	if _, err := NewPaymentRepository(testDB).FindLatestByBudgetID(ctx, b.ID()); err == nil {
		t.Fatal("pagamento do Do interno deveria ter sido desfeito com a transação externa")
	}
}

func TestUnitOfWork_PayloadInvalido(t *testing.T) {
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	err := uow.Do(context.Background(), func(ctx context.Context, tx ports.Tx) error {
		return tx.Events().Publish(ctx, "x", contracts.Meta{}, func() {})
	})
	if err == nil {
		t.Fatal("payload não serializável deveria falhar")
	}
}

func TestInbox_CasoDeUsoParticipaDaTransacaoDoInbox(t *testing.T) {
	ctx := context.Background()
	uow := NewUnitOfWork(testDB, topic, "billing-service")
	in := NewInbox(testDB, "billing-commands-test")
	b := seedBudget(t, false)
	env, _ := contracts.NewEnvelope(contracts.CmdCancelBudget, "os-service", contracts.Meta{SagaID: b.SagaID(), OSID: b.OSID()}, contracts.CancelBudget{BudgetID: b.ID()})
	t.Cleanup(func() { testDB.Exec("DELETE FROM processed_messages WHERE message_id = ?", env.MessageID) })

	cancelBudget := func(ctx context.Context) error {
		return uow.Do(ctx, func(ctx context.Context, tx ports.Tx) error {
			got, err := tx.Budgets().FindByID(ctx, b.ID())
			if err != nil {
				return err
			}
			if err := got.Cancel("compensação", now); err != nil {
				return err
			}
			if err := tx.Budgets().Update(ctx, got); err != nil {
				return err
			}
			return tx.Events().Publish(ctx, contracts.EvtBudgetCancelled, env.Reply(), contracts.BudgetCancelled{BudgetID: b.ID()})
		})
	}

	// Primeira tentativa falha depois de gravar: inbox, estado e outbox são desfeitos juntos.
	boom := errors.New("falha transitória")
	_, err := in.Once(ctx, env, func(ctx context.Context) error {
		if err := cancelBudget(ctx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("veio %v", err)
	}
	if len(outboxFor(t, b.SagaID())) != 0 {
		t.Fatal("rollback do inbox deveria desfazer o outbox do caso de uso")
	}

	// Reentrega processa; a seguinte é descartada como duplicata.
	dup, err := in.Once(ctx, env, cancelBudget)
	if err != nil || dup {
		t.Fatalf("primeira entrega efetiva: dup=%v err=%v", dup, err)
	}
	dup, err = in.Once(ctx, env, func(context.Context) error { t.Fatal("não pode rodar de novo"); return nil })
	if err != nil || !dup {
		t.Fatalf("reentrega: dup=%v err=%v", dup, err)
	}
	msgs := outboxFor(t, b.SagaID())
	if len(msgs) != 1 || msgs[0].Type != contracts.EvtBudgetCancelled {
		t.Fatalf("outbox: %+v", msgs)
	}
	reloaded, _ := NewBudgetRepository(testDB).FindByID(ctx, b.ID())
	if reloaded.Status() != entities.BudgetCancelled {
		t.Fatalf("status = %s", reloaded.Status())
	}
}
