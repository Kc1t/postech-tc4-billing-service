package postgresql

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/inbox"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/messaging"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/outbox"
	"gorm.io/gorm"
)

// txKey guarda no context a transação corrente (transação "ambiente"): é assim que o caso de
// uso, chamado de dentro do Inbox, grava estado + outbox + registro do inbox no mesmo commit.
type txKey struct{}

func withTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

func txFrom(ctx context.Context) *gorm.DB {
	tx, _ := ctx.Value(txKey{}).(*gorm.DB)
	return tx
}

// UnitOfWork implementa ports.UnitOfWork sobre transações do GORM. Os eventos publicados
// dentro da transação vão para o outbox com destino no tópico de eventos do serviço.
type UnitOfWork struct {
	db       *gorm.DB
	events   messaging.Destination
	producer string
}

// NewUnitOfWork cria a unidade de trabalho. eventsTopicARN é o tópico SNS billing-events;
// producer vai no campo "producer" dos envelopes.
func NewUnitOfWork(db *gorm.DB, eventsTopicARN, producer string) *UnitOfWork {
	return &UnitOfWork{
		db:       db,
		events:   messaging.Destination{Kind: messaging.Topic, Name: eventsTopicARN},
		producer: producer,
	}
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context, tx ports.Tx) error) error {
	if tx := txFrom(ctx); tx != nil {
		return fn(ctx, u.scope(tx))
	}
	return u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(withTx(ctx, tx), u.scope(tx))
	})
}

func (u *UnitOfWork) scope(tx *gorm.DB) ports.Tx {
	return &txScope{
		budgets:  NewBudgetRepository(tx),
		payments: NewPaymentRepository(tx),
		events:   &outboxPublisher{tx: tx, dest: u.events, producer: u.producer},
	}
}

type txScope struct {
	budgets  ports.BudgetRepository
	payments ports.PaymentRepository
	events   ports.EventPublisher
}

func (s *txScope) Budgets() ports.BudgetRepository   { return s.budgets }
func (s *txScope) Payments() ports.PaymentRepository { return s.payments }
func (s *txScope) Events() ports.EventPublisher      { return s.events }

// outboxPublisher grava o evento no outbox (pkg/outbox) dentro da transação.
type outboxPublisher struct {
	tx       *gorm.DB
	dest     messaging.Destination
	producer string
}

func (p *outboxPublisher) Publish(ctx context.Context, msgType string, meta contracts.Meta, payload any) error {
	env, err := contracts.NewEnvelope(msgType, p.producer, meta, payload)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, p.tx, p.dest, env)
}

// Inbox implementa ports.Inbox sobre pkg/inbox, deixando a transação no context para os casos
// de uso participarem dela.
type Inbox struct {
	db       *gorm.DB
	consumer string
}

// NewInbox cria o inbox de um consumidor (o nome entra na chave de idempotência).
func NewInbox(db *gorm.DB, consumer string) *Inbox { return &Inbox{db: db, consumer: consumer} }

func (i *Inbox) Once(ctx context.Context, env contracts.Envelope, fn func(ctx context.Context) error) (bool, error) {
	return inbox.Once(ctx, i.db, i.consumer, env, func(tx *gorm.DB) error {
		return fn(withTx(ctx, tx))
	})
}
