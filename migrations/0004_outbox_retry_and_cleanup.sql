-- Espera exponencial entre tentativas do relay e limpeza do outbox e do inbox (pkg/outbox, ADR-0018
-- do os-service). Os nomes dos índices são os que o AutoMigrate do chassis daria, para os testes do
-- chassis e esta migration convivirem no mesmo banco sem duplicar índice.
ALTER TABLE outbox_messages ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;
CREATE INDEX IF NOT EXISTS idx_outbox_messages_next_attempt_at ON outbox_messages (next_attempt_at);

-- A limpeza apaga o inbox por data (processed_at < agora - retenção).
CREATE INDEX IF NOT EXISTS idx_processed_messages_processed_at ON processed_messages (processed_at);
