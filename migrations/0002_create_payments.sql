-- Pagamentos: cada linha é uma tentativa de cobrança de um orçamento aprovado.
CREATE TABLE payments (
    id                     uuid          PRIMARY KEY,
    budget_id              uuid          NOT NULL REFERENCES budgets (id),
    os_id                  text          NOT NULL,
    amount                 numeric(12,2) NOT NULL CHECK (amount >= 0),
    status                 varchar(20)   NOT NULL
                           CHECK (status IN ('created', 'pending', 'approved', 'failed', 'cancelled', 'refunded')),
    provider               varchar(20)   NOT NULL CHECK (provider IN ('mercadopago', 'fake')),
    provider_preference_id text,
    provider_payment_id    text,
    checkout_url           text,
    failure_reason         text          NOT NULL DEFAULT '',
    refunded_at            timestamptz,
    version                integer       NOT NULL DEFAULT 1,
    created_at             timestamptz   NOT NULL DEFAULT now(),
    updated_at             timestamptz   NOT NULL DEFAULT now()
);

-- O ID do pagamento no provedor é único quando presente (o webhook chega por ele).
CREATE UNIQUE INDEX ux_payments_provider_payment_id
    ON payments (provider_payment_id) WHERE provider_payment_id IS NOT NULL;

-- No máximo uma cobrança "viva" (em aberto ou paga) por orçamento: duas entregas concorrentes
-- de RequestPayment não geram dois links.
CREATE UNIQUE INDEX ux_payments_live_per_budget
    ON payments (budget_id) WHERE status IN ('created', 'pending', 'approved');

CREATE INDEX idx_payments_budget_created ON payments (budget_id, created_at DESC);
