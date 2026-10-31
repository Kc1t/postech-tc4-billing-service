-- Dados de demonstração do billing-service.
-- Aplicar depois das migrations (o serviço as aplica ao subir):
--   psql "$POSTGRES_DSN" -f scripts/seed.sql        (ou: make seed)
-- Idempotente: pode rodar várias vezes.
--
-- Cliente de exemplo: CPF 529.982.247-25 (mesmo CPF de teste usado no os-service). Gere um
-- token de cliente com: go run ./cmd/gentoken -role client -document 52998224725
BEGIN;

-- 1) Orçamento aguardando aprovação do cliente.
INSERT INTO budgets (id, os_id, os_code, saga_id, requester_id, requester_document, requester_name,
                     requester_email, items, total, status, version, created_at, updated_at)
VALUES ('6f1c2a7e-0001-4a51-9a3e-000000000001', 'seed-os-0001', 9001, 'seed-saga-0001',
        'seed-requester-1', '52998224725', 'Cliente Demonstração', 'cliente.demo@example.com',
        '[{"kind":"service","ref":"1","description":"Revisão completa","quantity":1,"unit_price":350.00},
          {"kind":"part","ref":"OIL-5W30","description":"Óleo 5W30 (litro)","quantity":4,"unit_price":42.50}]',
        520.00, 'pending_approval', 1, now(), now())
ON CONFLICT (id) DO NOTHING;

-- 2) Orçamento aprovado com cobrança aberta no gateway fake (pronto para POST /payments/{id}/simulate).
INSERT INTO budgets (id, os_id, os_code, saga_id, requester_id, requester_document, requester_name,
                     requester_email, items, total, status, decided_at, version, created_at, updated_at)
VALUES ('6f1c2a7e-0002-4a51-9a3e-000000000002', 'seed-os-0002', 9002, 'seed-saga-0002',
        'seed-requester-1', '52998224725', 'Cliente Demonstração', 'cliente.demo@example.com',
        '[{"kind":"service","ref":"2","description":"Troca de pastilhas","quantity":1,"unit_price":180.00},
          {"kind":"part","ref":"PST-DIANT","description":"Jogo de pastilhas dianteiras","quantity":1,"unit_price":219.90}]',
        399.90, 'approved', now(), 2, now(), now())
ON CONFLICT (id) DO NOTHING;

INSERT INTO payments (id, budget_id, os_id, amount, status, provider, provider_preference_id,
                      checkout_url, version, created_at, updated_at)
VALUES ('7a2d3b8f-0002-4b62-8b4f-000000000002', '6f1c2a7e-0002-4a51-9a3e-000000000002', 'seed-os-0002',
        399.90, 'pending', 'fake', 'fake-pref-7a2d3b8f-0002-4b62-8b4f-000000000002',
        'http://localhost:8082/fake-checkout/7a2d3b8f-0002-4b62-8b4f-000000000002', 2, now(), now())
ON CONFLICT (id) DO NOTHING;

COMMIT;
