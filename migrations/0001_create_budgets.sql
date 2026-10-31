-- Orçamentos: um por ordem de serviço (os_id único). O documento do cliente é guardado para a
-- checagem de posse (claim "document" do JWT); nunca vai para logs.
CREATE TABLE budgets (
    id                 uuid          PRIMARY KEY,
    os_id              text          NOT NULL,
    os_code            integer       NOT NULL CHECK (os_code > 0),
    saga_id            text          NOT NULL,
    requester_id       text          NOT NULL,
    requester_document varchar(14)   NOT NULL,
    requester_name     text          NOT NULL,
    requester_email    text          NOT NULL,
    items              jsonb         NOT NULL CHECK (jsonb_typeof(items) = 'array' AND jsonb_array_length(items) > 0),
    total              numeric(12,2) NOT NULL CHECK (total >= 0),
    status             varchar(20)   NOT NULL
                       CHECK (status IN ('pending_approval', 'approved', 'rejected', 'cancelled')),
    decided_at         timestamptz,
    reason             text          NOT NULL DEFAULT '',
    version            integer       NOT NULL DEFAULT 1,
    created_at         timestamptz   NOT NULL DEFAULT now(),
    updated_at         timestamptz   NOT NULL DEFAULT now(),
    CONSTRAINT budgets_os_id_key UNIQUE (os_id)
);

CREATE INDEX idx_budgets_requester_document ON budgets (requester_document);
CREATE INDEX idx_budgets_status ON budgets (status);
