package ports

import (
	"context"

	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
)

//go:generate mockgen -source=./transaction.go -destination=./mocks/transaction.go -package=mocks

// EventPublisher registra um evento de domínio para publicação no tópico billing-events.
// A implementação grava no outbox, dentro da transação corrente: o evento só existe se o estado
// que o originou também foi salvo (Transactional Outbox).
type EventPublisher interface {
	Publish(ctx context.Context, msgType string, meta contracts.Meta, payload any) error
}

// Tx dá acesso aos repositórios e ao outbox ligados a uma mesma transação.
type Tx interface {
	Budgets() BudgetRepository
	Payments() PaymentRepository
	Events() EventPublisher
}

// UnitOfWork delimita a transação de um caso de uso.
type UnitOfWork interface {
	// Do executa fn numa transação: commit se fn devolver nil, rollback caso contrário. Se ctx já
	// carrega uma transação (a do Inbox, por exemplo), fn participa dela em vez de abrir outra.
	Do(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Inbox garante o processamento único de cada mensagem consumida (Idempotent Consumer).
type Inbox interface {
	// Once executa fn uma única vez por message_id, na mesma transação que registra a mensagem
	// como processada. O ctx passado a fn carrega essa transação, então UnitOfWork.Do dentro de
	// fn participa dela. duplicate=true indica reentrega já processada (fn não rodou).
	Once(ctx context.Context, env contracts.Envelope, fn func(ctx context.Context) error) (duplicate bool, err error)
}
