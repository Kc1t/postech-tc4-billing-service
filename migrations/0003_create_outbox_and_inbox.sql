-- Tabelas do chassis (pkg/outbox e pkg/inbox). As colunas espelham os modelos GORM do chassis;
-- IF NOT EXISTS permite que os testes do chassis (que usam AutoMigrate) e esta migration
-- convivam no mesmo banco.
CREATE TABLE IF NOT EXISTS outbox_messages (
    id               uuid        NOT NULL PRIMARY KEY,
    destination_kind text        NOT NULL,
    destination      text        NOT NULL,
    type             text        NOT NULL,
    saga_id          text,
    body             text        NOT NULL,
    trace_headers    text,
    created_at       timestamptz NOT NULL,
    published_at     timestamptz,
    attempts         bigint      NOT NULL DEFAULT 0,
    last_error       text
);

CREATE INDEX IF NOT EXISTS idx_outbox_messages_created_at ON outbox_messages (created_at);
CREATE INDEX IF NOT EXISTS idx_outbox_messages_published_at ON outbox_messages (published_at);
CREATE INDEX IF NOT EXISTS idx_outbox_messages_saga_id ON outbox_messages (saga_id);
CREATE INDEX IF NOT EXISTS idx_outbox_messages_type ON outbox_messages (type);
-- O relay só lê pendentes: índice parcial mantém a varredura barata com o outbox crescendo.
CREATE INDEX IF NOT EXISTS idx_outbox_messages_pending ON outbox_messages (created_at) WHERE published_at IS NULL;

CREATE TABLE IF NOT EXISTS processed_messages (
    message_id   text        NOT NULL,
    consumer     text        NOT NULL,
    type         text        NOT NULL,
    processed_at timestamptz NOT NULL,
    PRIMARY KEY (message_id, consumer)
);
