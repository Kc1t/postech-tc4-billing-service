# billing-service — Orçamento e Pagamento

Microsserviço da Fase 4 do Tech Challenge FIAP SOAT (oficina mecânica). Cuida do **orçamento** da ordem de serviço e do **pagamento** pelo Mercado Pago (Checkout Pro). Participa da **saga orquestrada** pelo `os-service`: recebe comandos pela fila `billing-commands` e responde com eventos no tópico `billing-events`.

- Go 1.25, Gin, GORM + PostgreSQL próprio, SNS/SQS (LocalStack em desenvolvimento), New Relic e logs JSON (`log/slog`).
- Arquitetura hexagonal, no mesmo desenho do `os-service`, e chassis de mensageria/outbox/inbox/observabilidade copiado de lá (`pkg/`).
- Cobertura de testes de **96,5%** (sem contar `cmd/`, `docs/`, `mocks/` e `test/`), com repositórios testados contra Postgres real e um teste de componente com Postgres + LocalStack.

## Sumário

1. [Responsabilidades](#responsabilidades)
2. [Arquitetura](#arquitetura)
3. [Mensageria](#mensageria)
4. [API HTTP](#api-http)
5. [Erros](#erros)
6. [Modelo de dados](#modelo-de-dados)
7. [Integração com o Mercado Pago](#integração-com-o-mercado-pago)
8. [Configuração](#configuração)
9. [Como rodar](#como-rodar)
10. [Como testar](#como-testar)
11. [Observabilidade](#observabilidade)
12. [Kubernetes](#kubernetes)
13. [Decisões e limitações](#decisões-e-limitações)

## Responsabilidades

| Faz | Não faz |
|---|---|
| Cria o orçamento da OS a partir dos itens e preços enviados pelo `os-service` (idempotente por `os_id`) | Calcular preço ou consultar catálogo e estoque (é do `os-service`) |
| Deixa o cliente dono (CPF do token) aprovar ou recusar o orçamento | Controlar o andamento da saga, prazos ou timeouts (é do orquestrador) |
| Gera o link de pagamento (Mercado Pago ou fake) e acompanha o resultado pelo webhook | Notificar o cliente por e-mail (é da `notification-lambda`) |
| Estorna o pagamento aprovado e cancela orçamento e pagamento pendente (compensações) | Executar diagnóstico e reparo (é do `execution-service`) |
| Publica um evento para cada mudança de estado, sempre pelo outbox | |

## Arquitetura

```mermaid
flowchart LR
    subgraph inbound["Adapters de entrada"]
        SQS[["SQS billing-commands<br/>messaging.Consumer + inbox"]]
        HTTP["HTTP /api/v1<br/>Gin + JWT + CORS"]
        WH["Webhook Mercado Pago<br/>x-signature HMAC"]
    end
    subgraph app["Aplicação"]
        UCB["usecase/budget<br/>create · approve · reject · cancel · get"]
        UCP["usecase/payment<br/>request · refund · notification · simulate · get"]
    end
    subgraph domain["Domínio"]
        E["Budget · Payment<br/>máquinas de estado, posse, dinheiro em centavos"]
    end
    subgraph outbound["Adapters de saída"]
        PG[("PostgreSQL<br/>budgets · payments<br/>outbox · inbox")]
        MP["Mercado Pago<br/>pkg/httpclient: timeout, retry, breaker"]
        FAKE["Gateway fake"]
        RELAY[["outbox.Relay → SNS billing-events"]]
    end
    SQS --> UCB & UCP
    HTTP --> UCB & UCP
    WH --> UCP
    UCB & UCP --> E
    UCB & UCP -- "ports.UnitOfWork / repositórios" --> PG
    UCP -- "ports.PaymentGateway" --> MP & FAKE
    PG --> RELAY
```

- `internal/domain`: entidades `Budget` e `Payment` com as transições válidas, regra de posse (`CheckReadAccess`/`CheckDecisionAccess`), mascaramento de CPF e dinheiro somado em centavos. Erros sentinela em `internal/domain/errors`.
- `internal/ports`: interfaces de repositórios, unidade de trabalho, inbox, gateway de pagamento, verificador de webhook e casos de uso. Os mocks em `internal/ports/mocks` são gerados com `mockgen` (`make mocks`).
- `internal/application/usecase/{budget,payment}`: um arquivo por operação.
- `internal/adapters/inbound`: `messaging` (traduz envelopes em comandos) e `http` (handlers, presenter e erro padronizado).
- `internal/adapters/outbound`: `postgresql` (repositórios com optimistic lock, unit of work com outbox, inbox, migrations), `mercadopago` (HTTP direto, sem SDK) e `fakegateway`.
- `cmd/api`: `main` (desligamento gracioso), `bootstrap` (injeção de dependências e ciclo de vida), `routes` e `middleware`. `cmd/gentoken` emite JWT de desenvolvimento.
- `pkg/`: chassis copiado do `os-service` (`contracts`, `messaging`, `outbox`, `inbox`, `httpclient`, `awsx`, `logger`, `observability`, `env`).

### O billing na saga

```mermaid
sequenceDiagram
    autonumber
    participant OS as os-service (orquestrador)
    participant B as billing-service
    participant C as Cliente
    participant MP as Mercado Pago
    OS->>B: billing.CreateBudget (itens, total, requester)
    B-->>OS: BudgetCreated | BudgetCreationFailed
    C->>B: POST /budgets/{id}/approve (JWT do CPF)
    B-->>OS: BudgetApproved | BudgetRejected
    OS->>B: billing.RequestPayment
    B->>MP: POST /checkout/preferences
    B-->>OS: PaymentLinkCreated (checkout_url)
    C->>MP: paga no Checkout Pro
    MP->>B: POST /webhooks/mercadopago (assinado)
    B->>MP: GET /v1/payments/{id}
    B-->>OS: PaymentApproved | PaymentFailed
    Note over OS,B: compensações
    OS->>B: billing.RefundPayment
    B->>MP: POST /v1/payments/{id}/refunds
    B-->>OS: PaymentRefunded
    OS->>B: billing.CancelBudget
    B-->>OS: BudgetCancelled
```

## Mensageria

Os envelopes seguem `pkg/contracts` (`message_id`, `type`, `version`, `saga_id`, `os_id`, `correlation_id`, `causation_id`, `producer`, `occurred_at`, `payload`). Todo evento publicado leva o `causation_id` do comando que o causou e repete `saga_id`, `os_id` e `correlation_id`.

### Comandos consumidos (fila `billing-commands`)

| Comando | Efeito | Eventos |
|---|---|---|
| `billing.CreateBudget` | Cria o orçamento `pending_approval`. Se a OS já tem orçamento, devolve o mesmo (idempotente por `os_id`). Itens ou total inválidos não criam nada. `simulate_failure=budget` força a falha. | `BudgetCreated{budget_id,total}` ou `BudgetCreationFailed{reason}` |
| `billing.RequestPayment` | Exige orçamento `approved`. Cria o pagamento e a preferência no gateway e guarda o `checkout_url`. Se já existe pagamento pendente ou aprovado, republica o evento correspondente. `simulate_failure=payment` falha o pagamento na hora. | `PaymentLinkCreated{budget_id,payment_id,checkout_url}` ou `PaymentFailed{reason}` |
| `billing.CancelBudget` | Orçamento vai para `cancelled`. Pagamento aberto também vai para `cancelled` e é cancelado no gateway. Se já estava cancelado, só republica. | `BudgetCancelled` |
| `billing.RefundPayment` | Estorna o pagamento aprovado no gateway, com `X-Idempotency-Key` igual ao id do pagamento, e marca como `refunded`. Se já estava estornado, só republica. | `PaymentRefunded{budget_id,payment_id,amount}` |

O `simulate_failure` só vale com `FAULT_INJECTION_ENABLED=true`. Com a flag desligada, o campo é ignorado.

### Eventos publicados (tópico `billing-events`)

`billing.BudgetCreated`, `billing.BudgetCreationFailed`, `billing.BudgetApproved`, `billing.BudgetRejected`, `billing.BudgetCancelled`, `billing.PaymentLinkCreated`, `billing.PaymentApproved`, `billing.PaymentFailed` e `billing.PaymentRefunded`. Os payloads estão em `pkg/contracts`. A aprovação e a recusa chegam pela API e o resultado do pagamento chega pelo webhook (ou pelo `simulate`); essas mudanças também geram eventos.

### Garantias

- **Outbox transacional**: o evento é gravado em `outbox_messages` na mesma transação que muda o estado. O `outbox.Relay` publica no SNS em lotes com `FOR UPDATE SKIP LOCKED`, então várias réplicas convivem. Se o processo cair, o evento sai na próxima varredura.
- **Inbox (consumidor idempotente)**: cada comando roda dentro de `inbox.Once`, que registra o `message_id` em `processed_messages` no mesmo commit do efeito. Uma reentrega do SQS é descartada (`message_duplicate_skipped`).
- **Erro só para falha transitória**: banco fora ou gateway indisponível devolvem erro, e a mensagem volta para a fila até ir para a DLQ (`maxReceiveCount`). Regra de negócio violada vira evento de falha. Payload malformado também devolve erro e acaba na DLQ para análise.
- **Concorrência**: `version` em `budgets` e `payments` (optimistic lock) e índices únicos parciais impedem dois pagamentos vivos para o mesmo orçamento ou o mesmo `provider_payment_id` em dois registros.

## API HTTP

Swagger em `/swagger/index.html`, gerado em `docs/` (`make swagger`). Autenticação por `Authorization: Bearer <JWT HS256>` com `JWT_SECRET` compartilhado e claims `sub`, `role` (`admin` | `mechanic` | `client`), `document` (CPF, só no token de cliente) e `exp` (obrigatório).

| Método e rota | Quem | Descrição |
|---|---|---|
| `GET /health/live` | público | processo de pé (liveness) |
| `GET /health/ready` | público | banco e SQS acessíveis (readiness), com timeout por checagem |
| `GET /api/v1/budgets/os/{os_id}` | admin; client dono | orçamento da OS com itens, total, status e pagamento (usado pela API composition do `os-service`) |
| `GET /api/v1/budgets/{id}` | admin; client dono | idem, por id |
| `POST /api/v1/budgets/{id}/approve` | client dono | `pending_approval → approved` e publica `BudgetApproved` |
| `POST /api/v1/budgets/{id}/reject` | client dono, corpo `{"reason": "..."}` | `pending_approval → rejected` e publica `BudgetRejected` |
| `GET /api/v1/payments/{id}` | admin; client dono | status do pagamento e `checkout_url` |
| `POST /api/v1/payments/{id}/simulate` | admin, só com `PAYMENT_GATEWAY=fake` | corpo `{"status": "approved" \| "rejected"}`: simula o retorno do provedor pelas mesmas regras do webhook |
| `POST /api/v1/webhooks/mercadopago` | público, com assinatura obrigatória | notificação do Mercado Pago |
| `GET /fake-checkout/{payment_id}` | público, só com o gateway fake | página local que faz o papel do checkout |

"Dono" é o cliente cujo `document` no token é igual ao `requester_document` do orçamento (comparado só pelos dígitos). Mecânico não consulta cobrança. Admin consulta tudo, mas não decide no lugar do cliente. A resposta nunca traz o CPF.

## Erros

Toda resposta de erro tem o formato `{"error": {"code": "BUDGET_NOT_FOUND", "message": "orçamento não encontrado"}}`. Erros não mapeados viram 500 com mensagem genérica, e o detalhe fica só no log.

| HTTP | `code` | Quando |
|---|---|---|
| 400 | `INVALID_REQUEST` | corpo inválido, `reason` vazio, `status` do simulate fora de `approved`/`rejected` |
| 401 | `UNAUTHORIZED` | token ausente, expirado, sem `exp`, com algoritmo diferente de HS256 ou papel desconhecido |
| 401 | `INVALID_SIGNATURE` | webhook com `x-signature` ausente ou inválida |
| 403 | `FORBIDDEN` | papel sem acesso à rota ou cliente que não é o dono |
| 403 | `ORIGIN_NOT_ALLOWED` | preflight CORS de origem fora da allowlist |
| 403 | `SIMULATION_DISABLED` | `simulate` com o gateway Mercado Pago |
| 404 | `BUDGET_NOT_FOUND` / `PAYMENT_NOT_FOUND` | recurso inexistente |
| 404 | `NOT_FOUND` | rota inexistente |
| 409 | `INVALID_TRANSITION` | transição de status inválida (ex.: aprovar orçamento já aprovado) |
| 409 | `BUDGET_NOT_APPROVED` | operação que exige orçamento aprovado |
| 409 | `CONCURRENT_UPDATE` | conflito de optimistic lock: releia e tente de novo |
| 503 | `GATEWAY_UNAVAILABLE` | Mercado Pago indisponível (no webhook, faz o MP reenviar) |
| 500 | `INTERNAL_ERROR` | erro inesperado |

## Modelo de dados

As migrations SQL versionadas ficam em `migrations/` e são aplicadas na subida com `pg_advisory_xact_lock`, o que é seguro com várias réplicas subindo juntas. O controle fica em `schema_migrations`. Dados de demonstração estão em `scripts/seed.sql` (`make seed`).

```mermaid
erDiagram
    budgets ||--o{ payments : "tentativas"
    budgets {
        uuid id PK
        text os_id UK
        int os_code
        text saga_id
        text requester_id
        text requester_document
        text requester_name
        text requester_email
        jsonb items
        numeric total "12,2"
        text status
        timestamptz decided_at
        text reason
        int version "optimistic lock"
    }
    payments {
        uuid id PK
        uuid budget_id FK
        text os_id
        numeric amount "12,2"
        text status
        text provider "mercadopago | fake"
        text provider_preference_id
        text provider_payment_id "único quando não nulo"
        text checkout_url
        text failure_reason
        timestamptz refunded_at
        int version
    }
```

Também existem `outbox_messages` e `processed_messages`, do chassis. Um índice único parcial garante no máximo um pagamento `created`/`pending`/`approved` por orçamento.

```mermaid
stateDiagram-v2
    direction LR
    state "Orçamento" as B {
        [*] --> pending_approval
        pending_approval --> approved: cliente aprova
        pending_approval --> rejected: cliente recusa
        pending_approval --> cancelled: CancelBudget
        approved --> cancelled: CancelBudget
    }
    state "Pagamento" as P {
        [*] --> created
        created --> pending: link criado
        created --> failed
        created --> cancelled
        pending --> approved: webhook/simulate
        pending --> failed: rejeitado
        pending --> cancelled: CancelBudget
        approved --> refunded: RefundPayment
    }
```

## Integração com o Mercado Pago

O adapter usa HTTP direto, sem SDK, em `internal/adapters/outbound/mercadopago`, e passa por `pkg/httpclient` (timeout por tentativa, retry com backoff só em métodos idempotentes e circuit breaker).

1. **Checkout Pro**: `POST /checkout/preferences` com um único item (o total do orçamento), `external_reference` igual ao id do nosso pagamento, `notification_url` igual a `MP_NOTIFICATION_URL` e expiração definida por `MP_PREFERENCE_TTL`. O link devolvido é o `init_point`.
2. **Webhook**: o handler valida `x-signature` (`ts=...,v1=...`) calculando o HMAC-SHA256 do manifest `id:{data.id};request-id:{x-request-id};ts:{ts};` com `MP_WEBHOOK_SECRET`. O `data.id` vem da query, em minúsculas. A comparação é em tempo constante e falha fechada: sem segredo configurado, toda notificação é recusada com 401. Com `MP_WEBHOOK_MAX_AGE > 0`, notificações com `ts` fora da janela também são recusadas.
3. **Fonte da verdade**: o corpo do webhook nunca é usado como verdade. O serviço sempre relê `GET /v1/payments/{id}` e acha o pagamento pelo `external_reference`. `approved` gera `PaymentApproved` se o valor bater com o do orçamento; valor divergente vira `PaymentFailed`. `rejected` e `cancelled` geram `PaymentFailed`, e para a saga `rejected` é falha definitiva. `pending` e `in_process` são ignorados. Notificações de outro tipo, IPN legado ou sem `data.id` recebem 200 e são ignoradas.
4. **Idempotência**: reentregas e as notificações `payment.created` e `payment.updated` do mesmo pagamento não repetem efeitos. Um pagamento aprovado que chega para uma tentativa já encerrada (órfão) é estornado automaticamente.
5. **Estorno e cancelamento**: estorno com `POST /v1/payments/{id}/refunds` e `X-Idempotency-Key` igual ao id do pagamento. Cancelamento com `PUT /v1/payments/{id}` e `{"status":"cancelled"}`.
6. **Classificação de erros**: 404 vira `ErrGatewayNotFound`. Os outros 4xx viram `ErrGatewayRejected` (definitivo, gera `PaymentFailed`). 401/403, 5xx, timeout e circuito aberto viram `ErrGatewayUnavailable` (transitório: retry do SQS ou 503 para o MP reenviar).

**Gateway fake** (`PAYMENT_GATEWAY=fake`, o padrão local): o `checkout_url` é `FAKE_CHECKOUT_BASE_URL/fake-checkout/{payment_id}` e o retorno do provedor é simulado com `POST /api/v1/payments/{id}/simulate`. Serve para desenvolvimento, BDD e demonstração sem credenciais.

## Configuração

Todas as variáveis estão em `.env.example`. Em desenvolvimento, `.env` é lido automaticamente. Na subida, `Config.Validate` recusa combinações inválidas e mostra todas de uma vez.

| Variável | Padrão | Descrição |
|---|---|---|
| `APP_PORT` / `APP_ENV` / `LOG_LEVEL` | `8082` / `development` / `info` | `APP_ENV=prod` exige `JWT_SECRET` próprio e mensageria configurada |
| `POSTGRES_DSN` | `postgres://postgres:postgres@localhost:5432/billing?sslmode=disable` | banco do serviço |
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` / `DB_CONN_MAX_LIFETIME` | `10` / `5` / `30m` | pool de conexões |
| `JWT_SECRET` | `change-me-in-production` | segredo HS256 compartilhado com `os-service` e Lambda |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000` | allowlist separada por vírgula (`*` é recusado) |
| `AWS_REGION` / `AWS_ENDPOINT_URL` | — | região e endpoint do LocalStack em dev (vazio na AWS) |
| `SQS_BILLING_COMMANDS_URL` / `SNS_BILLING_EVENTS_ARN` | — | sem os dois, a mensageria fica desligada e só a API HTTP sobe |
| `OUTBOX_POLL_INTERVAL` | `500ms` | intervalo do relay do outbox |
| `FAULT_INJECTION_ENABLED` | `false` | liga o `simulate_failure` dos comandos |
| `PAYMENT_GATEWAY` | `fake` | `mercadopago` ou `fake` |
| `MP_ACCESS_TOKEN` / `MP_WEBHOOK_SECRET` / `MP_NOTIFICATION_URL` | — | obrigatórios com `mercadopago` (use credenciais de teste) |
| `MP_BASE_URL` / `MP_TIMEOUT` / `MP_PREFERENCE_TTL` / `MP_WEBHOOK_MAX_AGE` | `https://api.mercadopago.com` / `5s` / `24h` / `0` | ajustes do adapter |
| `FAKE_CHECKOUT_BASE_URL` | `http://localhost:8082` | base do link do gateway fake |
| `NEW_RELIC_APP_NAME` / `NEW_RELIC_LICENSE_KEY` | `billing-service` / — | sem licença, o agente fica desligado |
| `READINESS_TIMEOUT` / `SHUTDOWN_TIMEOUT` | `2s` / `20s` | timeout das checagens e do desligamento gracioso |

## Como rodar

### Docker Compose (serviço + Postgres + LocalStack)

```bash
make up                      # docker compose up --build -d
make seed                    # opcional: orçamentos de demonstração
curl localhost:8082/health/ready
make logs                    # acompanha o serviço
make down
```

O LocalStack sobe com `deploy/localstack/init-aws.sh`, a mesma topologia de filas, tópicos e assinaturas do `os-service` e do Terraform. As portas do host podem ser trocadas com `BILLING_SERVICE_PORT`, `BILLING_POSTGRES_PORT` e `LOCALSTACK_PORT`.

### Local (go run)

```bash
cp .env.example .env         # ajuste POSTGRES_DSN e as URLs do LocalStack
make run
```

### Tokens e chamadas

```bash
ADMIN=$(go run ./cmd/gentoken -role admin)
CLIENT=$(go run ./cmd/gentoken -role client -document 52998224725)   # CPF do seed

curl -H "Authorization: Bearer $ADMIN" localhost:8082/api/v1/budgets/os/seed-os-0001
curl -X POST -H "Authorization: Bearer $CLIENT" \
  localhost:8082/api/v1/budgets/6f1c2a7e-0001-4a51-9a3e-000000000001/approve
```

Para simular a saga sem o `os-service`, publique um comando na fila:

```bash
aws --endpoint-url http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/billing-commands \
  --message-body '{"message_id":"'$(uuidgen)'","type":"billing.CreateBudget","version":1,"saga_id":"s-1","os_id":"os-1","producer":"os-service","occurred_at":"2026-11-01T12:00:00Z","payload":{"os_code":1,"requester":{"id":"r1","name":"Ana","email":"ana@example.com","document":"52998224725"},"items":[{"kind":"service","ref":"1","description":"Troca de óleo","quantity":1,"unit_price":120}],"total":120}}'
```

A collection `postman_collection.json` cobre todas as rotas. A requisição do webhook calcula a `x-signature` no pre-request a partir da variável `webhookSecret`.

## Como testar

| Comando | O que roda |
|---|---|
| `make test-infra` | sobe Postgres (porta `55433`) e LocalStack (porta `4567`) descartáveis |
| `make test` | todos os testes (`go test -p 1 ./...`), incluindo repositórios contra o Postgres de teste |
| `make test-unit` | só os pacotes que não usam banco (dispensa Docker) |
| `make test-component` | teste de componente (`-tags component`) com Postgres + LocalStack |
| `make cover` | cobertura sem `cmd/`, `docs/`, `mocks/` e `test/`, falhando abaixo de 80% |
| `make lint` | `golangci-lint` v2 (`.golangci.yml`) |
| `make test-infra-down` | remove os containers de teste |

Variáveis dos testes de banco: `TEST_DB_HOST`, `TEST_DB_PORT`, `TEST_DB_USER`, `TEST_DB_PASSWORD` e `TEST_DB_NAME`. O Makefile já usa `localhost:55433`, `postgres/postgres` e `billing_test`. O `-p 1` serializa os pacotes porque os testes de repositório, outbox e inbox dividem o mesmo banco. O teste de componente também usa `AWS_ENDPOINT_URL`.

- **Unidade**: domínio (máquinas de estado, dinheiro, CPF), casos de uso com gomock, handlers com `httptest`, middlewares (JWT com `alg=none`, HS512 e token expirado, CORS e correlação) e configuração.
- **Integração**: repositórios, unit of work, outbox e inbox contra Postgres real. Adapter do Mercado Pago contra `httptest`, com um vetor HMAC conhecido.
- **Componente** (`test/component`): sobe o serviço inteiro em processo. Cada execução cria um banco, fila, tópico e fila observadora únicos. Os cenários cobrem: caminho feliz com o gateway fake (`CreateBudget → BudgetCreated → aprovação HTTP → RequestPayment → PaymentLinkCreated → simulate → PaymentApproved → RefundPayment → PaymentRefunded`), reentrega e idempotência por `os_id`, recusa, cancelamento com pagamento pendente, fault injection e o adapter real do Mercado Pago contra uma API fake com webhook assinado e estorno com idempotency key.

Cobertura atual por pacote (`make cover`): domínio 99,4%, casos de uso 97,4% (budget) e 96,6% (payment), adapters HTTP e de mensageria 100%, Mercado Pago 97,4%, Postgres 92,3%, config 100%. Total: **96,2%**.

## CI/CD e qualidade

Pipeline próprio em [`.github/workflows/ci.yml`](.github/workflows/ci.yml), independente dos outros serviços. A `main` é protegida: só recebe PR, com os jobs abaixo obrigatórios.

| Job | O que faz |
|---|---|
| `quality` | `go mod tidy` sem diff, gofmt, `go vet` (também com a tag `component`), golangci-lint, gosec (falha a partir de severidade média), govulncheck e Swagger em dia com o código |
| `tests` | dependências de teste em container, `make cover` e cobertura mínima de 80% |
| `component` | teste de componente com Postgres e LocalStack (`make test-component`) |
| `sonarqube` | SonarQube Community num container do job, com quality gate próprio: cobertura ≥ 80%, duplicação ≤ 3% e nota A em confiabilidade, segurança e manutenibilidade |
| `terraform` | fmt, validate e tfsec do `infra/` |
| `image` | build da imagem e Trivy (CRITICAL com correção bloqueia) |
| `deploy` | só na `main`, depois de todos os anteriores e com `AWS_DEPLOY_ENABLED=true`: `terraform apply` do RDS (DSN lida do Secrets Manager), imagem no ECR, Secret e manifests no EKS, `rollout status` e smoke test em `/health/ready` pelo NLB |

**Evidência de cobertura:** **96,2%** no `make cover` e 95,4% no SonarQube, com o quality gate aprovado (0 bugs, 0 vulnerabilidades, 0 code smells, 0% de duplicação). Cada execução do CI publica a cobertura e a tabela do SonarQube no resumo do job.

## Observabilidade

- **Logs JSON** (`log/slog`). Cada requisição gera uma linha com `correlation_id`, rota, status, latência e `user_id`; as probes de health ficam de fora. O processamento de mensagens loga `saga_id`, `os_id`, `message_id`, `message_type`, `correlation_id` e `trace.id`/`span.id`. O CPF aparece sempre mascarado (`***.***.***-25`). O log do GORM usa SQL parametrizado, sem valores.
- **Correlação**: o `X-Correlation-ID` (ou `X-Request-ID`) é aceito ou gerado, devolvido na resposta e propagado no `correlation_id` dos eventos.
- **New Relic**: `nrgin` com a transação colocada no `context` da requisição, transações por mensagem consumida, segmentos SNS/SQS (`nrawssdk`) e trace distribuído nos atributos das mensagens. Sem `NEW_RELIC_LICENSE_KEY`, o agente fica desligado.

## Kubernetes

Os manifestos ficam em `k8s/` e não declaram namespace. Os `*_PLACEHOLDER` são substituídos pelo pipeline.

| Arquivo | Conteúdo |
|---|---|
| `deployment.yaml` | 2 réplicas, `RollingUpdate` com `maxSurge=1`/`maxUnavailable=0`, probes separadas (`startupProbe` e `livenessProbe` em `/health/live`, `readinessProbe` em `/health/ready`), requests/limits, `runAsNonRoot` (UID 10001), `readOnlyRootFilesystem`, sem capabilities e `preStop` de 5s |
| `service.yaml` | `ClusterIP` `billing-service` (API composition do `os-service`) e NLB `billing-service-public` (rotas `/api/v1/budgets`, `/payments`, `/webhooks/mercadopago` e `/fake-checkout` do API Gateway) |
| `configmap.yaml` / `secret.yaml` | configuração não sensível e modelo do Secret (`JWT_SECRET`, `POSTGRES_DSN`, `MP_ACCESS_TOKEN`, `MP_WEBHOOK_SECRET`, `NEW_RELIC_LICENSE_KEY`) |
| `hpa.yaml` / `pdb.yaml` | HPA de 2 a 6 réplicas (CPU 70%, memória 80%) e PDB `minAvailable: 1` |
| `networkpolicy.yaml` | entrada só na porta 8082; saída só para DNS, Postgres (5432) e HTTPS (443) |

## Decisões e limitações

- **A porta do gateway difere um pouco da spec**: `CreateCheckout` recebe um `CheckoutRequest` (ids, código da OS e valor) em vez da entidade, para o adapter não depender do domínio. `GetPayment` devolve a struct `ProviderPayment`, que inclui `status_detail` para o motivo da falha.
- **Chamada ao gateway dentro da transação do inbox**: a preferência é criada antes do commit. Se o commit falhar, a reentrega cria outra preferência, o que é inofensivo porque a antiga nunca é divulgada. Em troca, nunca existe pagamento gravado sem link.
- **Webhook síncrono**: valida, relê o pagamento e aplica o resultado na própria requisição. Falha transitória devolve 503 e o Mercado Pago reenvia.
- **401/403 do Mercado Pago contam como transitórios** (token rotacionado ou expirado não deve virar `PaymentFailed` definitivo). Um estorno recusado de forma definitiva vai para a DLQ para tratamento manual, porque não existe compensação da compensação.
- **Pagamento órfão**: um `approved` que chega para uma tentativa cancelada ou falha é estornado automaticamente (idempotency key `orphan-{id}`).
- **Rejeição definitiva**: o Mercado Pago permite tentar de novo depois de um `rejected`, mas para a saga o `rejected` encerra o pagamento (`PaymentFailed`).
- **Chassis**: o `pkg/` é copiado do `os-service` trocando só o import path, e passa pelo mesmo lint e gofmt do resto do código.
- **Limitações**: sem credenciais reais, o adapter do Mercado Pago é validado contra `httptest`, não contra o sandbox. O `MP_WEBHOOK_MAX_AGE` vem desligado por padrão para evitar recusas por diferença de relógio. O orçamento não expira sozinho: o prazo de aprovação é controlado pelo orquestrador, que envia `CancelBudget`.
