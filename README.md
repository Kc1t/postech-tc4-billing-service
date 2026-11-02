# postech-tc4-billing-service

Microsserviço de **orçamento e pagamento** da oficina mecânica. Gera o orçamento da ordem de serviço (OS), deixa o cliente aprovar ou recusar, cobra pelo Mercado Pago (Checkout Pro) e estorna quando a saga compensa. Participa da **saga orquestrada** pelo [os-service](https://github.com/Kc1t/postech-tc4-os-service): recebe comandos na fila `billing-commands` e responde com eventos no tópico `billing-events`.

Tech Challenge Fase 4, FIAP Pós Tech SOAT. Go 1.26, Gin, GORM sobre pgx, PostgreSQL próprio (RDS), SNS/SQS, New Relic, logs JSON (`log/slog`), Terraform e GitHub Actions.

| Repositório | Papel | Banco |
|---|---|---|
| [postech-tc4-os-service](https://github.com/Kc1t/postech-tc4-os-service) | OS, clientes, veículos, catálogo e estoque; orquestrador da saga | PostgreSQL (RDS) |
| **postech-tc4-billing-service** (este) | orçamento e pagamento com Mercado Pago | PostgreSQL (RDS) |
| [postech-tc4-execution-service](https://github.com/Kc1t/postech-tc4-execution-service) | fila de diagnóstico e reparo da oficina | DynamoDB |
| [postech-tc4-notification-lambda](https://github.com/Kc1t/postech-tc4-notification-lambda) | e-mail ao cliente a cada mudança de status da OS | DynamoDB (deduplicação) |
| [postech-tc4-lambda-auth](https://github.com/Kc1t/postech-tc4-lambda-auth) | CPF → JWT e authorizer do API Gateway | (consulta o os-service) |
| [postech-tc4-infra-k8s](https://github.com/Kc1t/postech-tc4-infra-k8s) | EKS, API Gateway, SNS/SQS/DLQ, observabilidade, e2e local | — |

## Guia rápido para avaliação

| Entregável da fase 4 | Onde está |
|---|---|
| Código-fonte | `cmd/`, `internal/`, `pkg/` ([Arquitetura](#arquitetura)) |
| Dockerfile e manifestos Kubernetes | [`Dockerfile`](Dockerfile) e [`k8s/`](k8s/) ([CI/CD e deploy](#cicd-e-deploy)) |
| Banco próprio | PostgreSQL no RDS, criado por [`infra/`](infra/); migrations em [`migrations/`](migrations/) ([Banco e modelo de dados](#banco-e-modelo-de-dados)) |
| Pipeline de CI/CD | [`.github/workflows/ci.yml`](.github/workflows/ci.yml) |
| Evidência de cobertura | **95,8%** no `go test` e **96,5%** no SonarQube, publicados no resumo de cada [execução do CI](https://github.com/Kc1t/postech-tc4-billing-service/actions/workflows/ci.yml) ([Testes](#testes-e-evidência-de-cobertura)) |
| Documentação da arquitetura do serviço | este README, de [Responsabilidades](#responsabilidades) a [Decisões e limitações](#decisões-e-limitações) |
| Swagger e Postman | [`docs/swagger.yaml`](docs/swagger.yaml), servido em `/swagger/index.html`, e [`postman_collection.json`](postman_collection.json) ([Swagger e Postman](#swagger-e-postman)) |

## Responsabilidades

| Faz | Não faz |
|---|---|
| Cria o orçamento da OS a partir dos itens e preços enviados pelo os-service (idempotente por `os_id`) | Calcular preço ou consultar catálogo e estoque (é do os-service) |
| Deixa o cliente dono (CPF do token) aprovar ou recusar o orçamento | Controlar a ordem das etapas, prazos e timeouts da saga (é do orquestrador) |
| Gera o link de pagamento (Mercado Pago ou gateway fake) e acompanha o resultado pelo webhook | Notificar o cliente por e-mail (é da notification-lambda) |
| Estorna o pagamento aprovado e cancela orçamento e pagamento pendente (compensações) | Executar diagnóstico e reparo (é do execution-service) |
| Publica um evento para cada mudança de estado, sempre pelo outbox | Acessar o banco de outro serviço |

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
        PG[("PostgreSQL<br/>budgets · payments<br/>outbox_messages · processed_messages")]
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

Arquitetura hexagonal, no mesmo desenho do os-service:

| Pasta | Conteúdo |
|---|---|
| `cmd/api` | `main` (desligamento gracioso), `bootstrap` (injeção de dependências e ciclo de vida), `routes` e `middleware`; `cmd/gentoken` emite JWT de desenvolvimento |
| `internal/domain` | entidades `Budget` e `Payment` com as transições válidas, regra de posse (`CheckReadAccess`, `CheckDecisionAccess`), mascaramento de CPF, dinheiro somado em centavos e erros sentinela |
| `internal/ports` | interfaces de repositórios, unidade de trabalho, inbox, gateway de pagamento, verificador do webhook e casos de uso; mocks gerados com `mockgen` (`make mocks`) |
| `internal/application/usecase/{budget,payment}` | um arquivo por operação |
| `internal/adapters/inbound` | `messaging` (envelope → comando) e `http` (handlers, presenter e erro padronizado) |
| `internal/adapters/outbound` | `postgresql` (repositórios com lock otimista, unit of work com outbox, inbox, migrations), `mercadopago` (HTTP direto, sem SDK) e `fakegateway` |
| `pkg/` | chassis copiado do os-service: `contracts`, `messaging`, `outbox`, `inbox`, `httpclient`, `awsx`, `logger`, `observability`, `env` |
| `migrations/` | SQL versionado, aplicado na subida |
| `infra/` | Terraform do RDS do serviço |
| `test/component` | teste de componente (build tag `component`) |

### O billing na saga

A saga é orquestrada pelo os-service ([ADR-0017](https://github.com/Kc1t/postech-tc4-os-service/blob/main/docs/adr/0017-saga-orquestrada-no-os-service.md)). O billing não conhece a ordem das etapas nem os outros serviços: executa o comando que recebe e responde com um evento.

```mermaid
sequenceDiagram
    autonumber
    participant OS as os-service (orquestrador)
    participant B as billing-service
    participant C as Cliente
    participant MP as Mercado Pago
    OS->>B: billing.CreateBudget (itens, total, cliente)
    B-->>OS: billing.BudgetCreated | billing.BudgetCreationFailed
    C->>B: POST /api/v1/budgets/{id}/approve (JWT do CPF)
    B-->>OS: billing.BudgetApproved | billing.BudgetRejected
    OS->>B: billing.RequestPayment
    B->>MP: POST /checkout/preferences
    B-->>OS: billing.PaymentLinkCreated (checkout_url)
    C->>MP: paga no Checkout Pro
    MP->>B: POST /api/v1/webhooks/mercadopago (assinado)
    B->>MP: GET /v1/payments/{id}
    B-->>OS: billing.PaymentApproved | billing.PaymentFailed
    Note over OS,B: compensações pedidas pelo orquestrador
    OS->>B: billing.RefundPayment
    B->>MP: POST /v1/payments/{id}/refunds
    B-->>OS: billing.PaymentRefunded
    OS->>B: billing.CancelBudget
    B-->>OS: billing.BudgetCancelled
```

## Comunicação

| Canal | Direção | Nome |
|---|---|---|
| HTTP síncrono | entrada | API Gateway → NLB `billing-service-public` (cliente, equipe, webhook); `Service billing-service` (ClusterIP) para a API composition do os-service |
| SQS | consome | fila `billing-commands` (DLQ `billing-commands-dlq` depois de 5 recebimentos) |
| SNS | publica | tópico `billing-events`, assinado pela fila `os-saga-replies` com raw delivery |
| HTTPS | saída | API do Mercado Pago (`MP_BASE_URL`) |

### API HTTP

Todas as rotas de `/api/v1`, menos o webhook, exigem `Authorization: Bearer <JWT>` (detalhes em [Segurança](#segurança)).

| Método e rota | Quem | Descrição |
|---|---|---|
| `GET /health/live` | público | processo de pé (liveness) |
| `GET /health/ready` | público | banco e SQS acessíveis (readiness), com timeout por checagem; `503` quando algum falha |
| `GET /api/v1/budgets/os/{os_id}` | admin; cliente dono | orçamento da OS com itens, total, status e pagamento (usado pela API composition do os-service) |
| `GET /api/v1/budgets/{id}` | admin; cliente dono | idem, por id |
| `POST /api/v1/budgets/{id}/approve` | cliente dono | `pending_approval → approved` e publica `billing.BudgetApproved` |
| `POST /api/v1/budgets/{id}/reject` | cliente dono, corpo `{"reason": "..."}` | `pending_approval → rejected` e publica `billing.BudgetRejected` |
| `GET /api/v1/payments/{id}` | admin; cliente dono | status do pagamento e `checkout_url` |
| `POST /api/v1/payments/{id}/simulate` | admin, só com `PAYMENT_GATEWAY=fake` | corpo `{"status": "approved" \| "rejected"}`: simula o retorno do provedor pelas mesmas regras do webhook |
| `POST /api/v1/webhooks/mercadopago` | público, assinatura obrigatória | notificação do Mercado Pago |
| `GET /fake-checkout/{payment_id}` | público, só com o gateway fake | página local que faz o papel do checkout |
| `GET /swagger/index.html` | público | Swagger UI |

O `X-Correlation-ID` (ou `X-Request-ID`) da requisição é aceito ou gerado, devolvido na resposta e copiado para o `correlation_id` dos eventos publicados. A resposta nunca traz o CPF.

#### Erros

Toda resposta de erro tem o formato `{"error": {"code": "BUDGET_NOT_FOUND", "message": "orçamento não encontrado"}}`. Erro não mapeado vira 500 com mensagem genérica, e o detalhe fica só no log.

| HTTP | `code` | Quando |
|---|---|---|
| 400 | `INVALID_REQUEST` | corpo inválido, `reason` vazio, `status` do simulate fora de `approved`/`rejected` |
| 401 | `UNAUTHORIZED` | token ausente, expirado, sem `exp`, com algoritmo diferente de HS256, sem `sub` ou com papel desconhecido |
| 401 | `INVALID_SIGNATURE` | webhook com `x-signature` ausente ou inválida |
| 403 | `FORBIDDEN` | papel sem acesso à rota ou cliente que não é o dono do orçamento |
| 403 | `ORIGIN_NOT_ALLOWED` | preflight CORS de origem fora da allowlist |
| 403 | `SIMULATION_DISABLED` | `simulate` com o gateway Mercado Pago |
| 404 | `BUDGET_NOT_FOUND` / `PAYMENT_NOT_FOUND` | recurso inexistente |
| 404 | `NOT_FOUND` | rota inexistente |
| 409 | `INVALID_TRANSITION` | transição de status inválida (ex.: aprovar orçamento já aprovado) |
| 409 | `BUDGET_NOT_APPROVED` | operação que exige orçamento aprovado |
| 409 | `CONCURRENT_UPDATE` | conflito de lock otimista: releia e tente de novo |
| 503 | `GATEWAY_UNAVAILABLE` | Mercado Pago indisponível (no webhook, faz o Mercado Pago reenviar) |
| 500 | `INTERNAL_ERROR` | erro inesperado |

### Swagger e Postman

- **Swagger**: [`docs/swagger.yaml`](docs/swagger.yaml) e [`docs/swagger.json`](docs/swagger.json), gerados das anotações dos handlers com `make swagger`. O serviço publica a UI em `/swagger/index.html` (`http://localhost:8082/swagger/index.html` no compose). O job `quality` do CI falha se o Swagger estiver desatualizado em relação ao código.
- **Postman**: [`postman_collection.json`](postman_collection.json) tem teste em toda requisição (74 requisições, 178 asserções) e não depende de seed nem de token gerado à mão. A pasta 0 faz login no os-service, cria cliente, veículo e catálogo, pega o JWT do cliente na Lambda auth-issuer pelo gateway local e diagnostica as OS no execution-service. Depois vêm:
  - orçamento: leitura pelo dono e pelo admin, outro cliente → 403, admin aprovando → 403, aprovar duas vezes → 409, id que não é UUID → 400 `INVALID_REQUEST`;
  - pagamento simulado no gateway fake e checkout fake;
  - webhook: notificação ignorada, sem assinatura → 401, assinatura inválida → 401 (HMAC calculado no pre-request);
  - compensações: orçamento cancelado, estorno, recusa sem cobrança, falha na criação do orçamento → 404.

  Variáveis em snake_case, com os mesmos nomes das outras collections (`base_url`, `os_url`, `exec_url`, `gateway_url`, `token`, `client_token`, `os_id`, `budget_id`, `payment_id`). Para rodar sozinha, suba a stack de [`postech-tc4-infra-k8s/local`](https://github.com/Kc1t/postech-tc4-infra-k8s/tree/main/local) (`make up && make lambdas && make gateway`) e use o Collection Runner; para rodar encadeada com as outras duas, `make postman` no mesmo diretório. O `scripts/seed.sql` e o `cmd/gentoken` continuam servindo para testar com curl sem os outros serviços.

### Mensagens consumidas (fila `billing-commands`)

Os envelopes seguem `pkg/contracts` (`message_id`, `type`, `version`, `saga_id`, `os_id`, `correlation_id`, `causation_id`, `producer`, `occurred_at`, `payload`), com JSON Schema por tipo em `pkg/contracts/schemas`, idêntico nos quatro repositórios Go e documentado no [AsyncAPI do os-service](https://github.com/Kc1t/postech-tc4-os-service/blob/main/docs/asyncapi/asyncapi.yaml).

| Comando | Payload | Efeito | Eventos publicados |
|---|---|---|---|
| `billing.CreateBudget` | `os_code`, `requester`, `items[]`, `total`, `simulate_failure` | Cria o orçamento `pending_approval`. Se a OS já tem orçamento, republica o mesmo (idempotente por `os_id`). Itens ou total inválidos não criam nada. `simulate_failure=budget` força a falha. | `billing.BudgetCreated{budget_id, total}` ou `billing.BudgetCreationFailed{reason}` |
| `billing.RequestPayment` | `budget_id`, `simulate_failure` | Exige orçamento `approved`. Cria o pagamento e a preferência no gateway e guarda o `checkout_url`. Se já existe pagamento pendente ou aprovado, republica o evento correspondente. `simulate_failure=payment` falha o pagamento na hora. | `billing.PaymentLinkCreated{budget_id, payment_id, checkout_url}` ou `billing.PaymentFailed{budget_id, payment_id, reason}` |
| `billing.CancelBudget` | `budget_id`, `reason` | Orçamento vai para `cancelled`. Pagamento aberto também vai para `cancelled` e é cancelado no gateway. Se já estava cancelado, só republica. | `billing.BudgetCancelled{budget_id, reason}` |
| `billing.RefundPayment` | `budget_id`, `reason` | Estorna o pagamento aprovado no gateway, com `X-Idempotency-Key` igual ao id do pagamento, e marca como `refunded`. Se já estava estornado, só republica. | `billing.PaymentRefunded{budget_id, payment_id, amount}` |

O `simulate_failure` só vale com `FAULT_INJECTION_ENABLED=true`; com a flag desligada, o campo é ignorado.

### Mensagens publicadas (tópico `billing-events`)

| Evento | Payload | Quando |
|---|---|---|
| `billing.BudgetCreated` | `budget_id`, `total` | resposta ao `CreateBudget` |
| `billing.BudgetCreationFailed` | `reason` | itens ou total inválidos, ou falha simulada |
| `billing.BudgetApproved` | `budget_id`, `approved_at` | cliente aprovou pela API |
| `billing.BudgetRejected` | `budget_id`, `reason` | cliente recusou pela API |
| `billing.BudgetCancelled` | `budget_id`, `reason` | resposta ao `CancelBudget` (compensação) |
| `billing.PaymentLinkCreated` | `budget_id`, `payment_id`, `checkout_url` | preferência criada no gateway |
| `billing.PaymentApproved` | `budget_id`, `payment_id`, `provider_payment_id`, `amount` | webhook ou `simulate` com pagamento aprovado e valor igual ao do orçamento |
| `billing.PaymentFailed` | `budget_id`, `payment_id`, `reason` | provedor recusou ou cancelou, valor divergente, checkout recusado, orçamento inexistente ou falha simulada |
| `billing.PaymentRefunded` | `budget_id`, `payment_id`, `amount` | resposta ao `RefundPayment` (compensação) |

Todo evento repete `saga_id`, `os_id` e `correlation_id` do comando e leva no `causation_id` o `message_id` do comando que o causou. Os atributos SNS (`message_type`, `message_id`, `saga_id`, `os_id` e os headers de trace do New Relic) viajam junto e, com raw delivery, chegam à fila `os-saga-replies`.

### Garantias de entrega

- **Outbox transacional**: o evento é gravado em `outbox_messages` na mesma transação que muda o estado. O `outbox.Relay` publica no SNS em lotes com `FOR UPDATE SKIP LOCKED`, então várias réplicas convivem. Uma mensagem que falha volta com espera exponencial (1 s, 2 s, 4 s... até 5 min, em `next_attempt_at`) sem travar o lote; a partir da 10ª tentativa cada falha vira `outbox_message_stuck`. O relay também apaga o outbox publicado há mais de 7 dias e o inbox com mais de 21 dias (mais que os 14 dias de retenção da DLQ, para um redrive antigo continuar deduplicado).
- **Inbox (consumidor idempotente)**: cada comando roda dentro de `inbox.Once`, que registra o `message_id` em `processed_messages` no mesmo commit do efeito. Uma reentrega do SQS é descartada (`message_duplicate_skipped`).
- **Erro só para falha transitória**: banco fora ou gateway indisponível devolvem erro, e a mensagem volta para a fila até ir para a DLQ (`maxReceiveCount` 5). Regra de negócio violada vira evento de falha. Payload fora do contrato também devolve erro (`payload_malformed`) e termina na DLQ para análise.
- **Concorrência**: coluna `version` em `budgets` e `payments` (lock otimista) e índices únicos parciais impedem dois pagamentos vivos para o mesmo orçamento e o mesmo `provider_payment_id` em dois registros.

### Mercado Pago

O adapter usa HTTP direto, sem SDK, em `internal/adapters/outbound/mercadopago`, e passa por `pkg/httpclient` (timeout por tentativa, retry com backoff só em métodos idempotentes e circuit breaker). Escolha do Checkout Pro e do gateway fake: [ADR-0020](https://github.com/Kc1t/postech-tc4-os-service/blob/main/docs/adr/0020-mercado-pago-checkout-pro.md).

1. **Checkout Pro**: `POST /checkout/preferences` com um único item (o total do orçamento), `external_reference` igual ao id do nosso pagamento, `notification_url` igual a `MP_NOTIFICATION_URL` e expiração definida por `MP_PREFERENCE_TTL`. O link devolvido é o `init_point`.
2. **Webhook**: o handler valida `x-signature` (`ts=...,v1=...`) calculando o HMAC-SHA256 do manifest `id:{data.id};request-id:{x-request-id};ts:{ts};` com `MP_WEBHOOK_SECRET`. O `data.id` vem da query, em minúsculas. A comparação é em tempo constante e falha fechada: sem segredo configurado, toda notificação de pagamento é recusada com 401. Com `MP_WEBHOOK_MAX_AGE > 0`, notificações com `ts` fora da janela também são recusadas.
3. **Fonte da verdade**: o corpo do webhook nunca é usado como verdade. O serviço relê `GET /v1/payments/{id}` e acha o pagamento pelo `external_reference`. `approved` gera `PaymentApproved` se o valor bater com o do orçamento; valor divergente vira `PaymentFailed`. `rejected` e `cancelled` geram `PaymentFailed`; para a saga, `rejected` é falha definitiva. `pending`, `in_process` e `authorized` só registram o id do provedor (`payment_in_analysis`). Notificações de outro tipo, IPN legado ou sem `data.id` recebem 200 e são ignoradas (`webhook_ignored`).
4. **Idempotência**: reentregas e as notificações `payment.created` e `payment.updated` do mesmo pagamento não repetem efeitos. Um pagamento aprovado que chega para uma tentativa já encerrada (órfão) é estornado automaticamente.
5. **Estorno e cancelamento**: estorno com `POST /v1/payments/{id}/refunds` e `X-Idempotency-Key` igual ao id do pagamento; cancelamento com `PUT /v1/payments/{id}` e `{"status":"cancelled"}`.
6. **Classificação de erros**: 404 vira `ErrGatewayNotFound`. Os outros 4xx viram `ErrGatewayRejected` (definitivo, gera `PaymentFailed`). 401/403, 5xx, timeout e circuito aberto viram `ErrGatewayUnavailable` (transitório: retry do SQS ou 503 para o Mercado Pago reenviar).

**Gateway fake** (`PAYMENT_GATEWAY=fake`, padrão local e do deploy): o `checkout_url` é `FAKE_CHECKOUT_BASE_URL/fake-checkout/{payment_id}` e o retorno do provedor é simulado com `POST /api/v1/payments/{id}/simulate`. Serve para desenvolvimento, e2e e demonstração sem credenciais.

## Banco e modelo de dados

PostgreSQL 16 próprio, criado pelo Terraform em [`infra/`](infra/): RDS `db.t3.micro` single-AZ ([ADR-0015](https://github.com/Kc1t/postech-tc4-os-service/blob/main/docs/adr/0015-rds-single-az-sem-deletion-protection.md)), banco `billing`, usuário `billing_app`, TLS obrigatório (`rds.force_ssl`), sem acesso público, security group que só aceita os nós do EKS e credenciais no Secrets Manager (`postech-tc4-prod-billing-service-db-credentials`). Nenhum outro serviço acessa este banco: o os-service lê o orçamento pela API ([ADR-0012](https://github.com/Kc1t/postech-tc4-os-service/blob/main/docs/adr/0012-banco-por-servico.md)).

Por que relacional: orçamento e tentativas de pagamento formam uma relação 1:N com integridade referencial, valores monetários exatos (`numeric(12,2)`) e regras que o banco garante sozinho (um orçamento por OS, no máximo um pagamento vivo por orçamento, `provider_payment_id` único), além da transação que grava estado, outbox e inbox juntos.

As migrations SQL versionadas ficam em [`migrations/`](migrations/) e são aplicadas na subida com `pg_advisory_xact_lock`, o que é seguro com várias réplicas subindo juntas. O controle fica em `schema_migrations`. Dados de demonstração estão em `scripts/seed.sql` (`make seed`).

```mermaid
erDiagram
    budgets ||--o{ payments : "tentativas"
    budgets {
        uuid id PK
        text os_id UK
        int os_code
        text saga_id
        text requester_id
        varchar requester_document "CPF, nunca em log"
        text requester_name
        text requester_email
        jsonb items
        numeric total "12,2"
        varchar status
        timestamptz decided_at
        text reason
        int version "lock otimista"
    }
    payments {
        uuid id PK
        uuid budget_id FK
        text os_id
        numeric amount "12,2"
        varchar status
        varchar provider "mercadopago | fake"
        text provider_preference_id
        text provider_payment_id "único quando não nulo"
        text checkout_url
        text failure_reason
        timestamptz refunded_at
        int version
    }
```

Também existem `outbox_messages` e `processed_messages` (chave `message_id` + `consumer`), do chassis. O índice único parcial `ux_payments_live_per_budget` garante no máximo um pagamento `created`, `pending` ou `approved` por orçamento.

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
        pending --> approved: webhook ou simulate
        pending --> failed: rejeitado
        pending --> cancelled: CancelBudget
        approved --> refunded: RefundPayment
    }
```

## Segurança

- **JWT revalidado no serviço**: o API Gateway já passou a requisição pelo Lambda authorizer, e o serviço valida o token de novo (`cmd/api/middleware/auth.go`): só HS256 (`alg=none` e troca de algoritmo são recusados), `exp` obrigatório, `sub` preenchido e `role` entre `admin`, `mechanic` e `client`. Falhou, 401. O segredo (`JWT_SECRET`) é o mesmo do os-service e da lambda-auth.
- **Papel por rota**: consulta para `admin` e `client`; aprovar e recusar só para `client`; `simulate` só para `admin`. Mecânico não acessa cobrança. Papel sem acesso recebe 403 `FORBIDDEN`.
- **Dono do recurso**: o cliente só consulta, aprova ou recusa o orçamento (e o pagamento) emitido para o próprio CPF. A claim `document` do token é comparada, só pelos dígitos, com o `requester_document` do orçamento; quem não é o dono recebe **403 `FORBIDDEN`**, mesmo com token válido. Admin consulta tudo, mas não decide no lugar do cliente.
- **Webhook sem JWT**: a autenticidade vem da assinatura HMAC (`x-signature`), comparada em tempo constante e com falha fechada; o resultado do pagamento é sempre relido na API do Mercado Pago.
- **CORS** só para as origens de `CORS_ALLOWED_ORIGINS`; `*` é recusado na subida.
- **Dados pessoais**: o CPF só aparece mascarado nos logs (`***.***.***-25`) e nunca nas respostas; o log do GORM registra SQL parametrizado, sem valores.
- **Execução**: container sem root (UID 10001), sistema de arquivos só leitura e sem capabilities; `NetworkPolicy` com entrada só na porta 8082 e saída só para DNS, Postgres (5432) e HTTPS (443); segredos no `Secret` do Kubernetes criado pelo CI a partir dos GitHub Secrets e do Secrets Manager.
- **Injeção de falha** (`simulate_failure`) só com `FAULT_INJECTION_ENABLED=true`.

## Observabilidade

New Relic (agente Go) e logs JSON no stdout, coletados no cluster pelo `nri-bundle`. Dashboard, alertas e SLOs ficam em Terraform no [postech-tc4-infra-k8s/observability](https://github.com/Kc1t/postech-tc4-infra-k8s/tree/main/observability).

- **APM**: `appName` `billing-service-postech` (`NEW_RELIC_APP_NAME` do ConfigMap). Transação web por rota (`nrgin`, com a transação colocada no `context` da requisição), transação `Consume/<tipo>` por mensagem consumida (atributos `messaging.consumer`, `message_type`, `message_id`, `saga_id`, `os_id`, `receive_count`) e `OutboxRelay/<tipo>` por mensagem publicada. Segmentos de banco do Postgres (`nrpgx5`), de SNS/SQS (`nrawssdk-v2`) e externos para `api.mercadopago.com`.
- **Trace distribuído**: o `Enqueue` do outbox guarda os headers W3C/New Relic da transação que gravou o evento, o relay os reaceita e o publicador os põe nos atributos da mensagem. O consumidor do os-service continua o mesmo trace.
- **Logs**: o texto vai na chave `message`; toda linha tem `service`, e as de requisição e mensageria têm `correlation_id`, `trace.id` e `span.id` (logs em contexto). Requisições geram a linha `request` (`method`, `path`, `route`, `status`, `latency_ms`, `user_id`), sem as probes de health. Mensagens trazem `saga_id`, `os_id`, `message_id` e `message_type`.

| Logs de negócio e de falha | Significado |
|---|---|
| `budget_created`, `budget_creation_failed`, `budget_approved`, `budget_rejected`, `budget_cancelled` | ciclo do orçamento |
| `payment_link_created`, `payment_approved`, `payment_failed` (`reason`), `payment_in_analysis`, `payment_refunded`, `refund_failed` | ciclo do pagamento |
| `orphan_payment_refunded` | pagamento aprovado para uma tentativa encerrada, estornado automaticamente |
| `webhook_processed`, `webhook_ignored`, `webhook_signature_invalid` | notificações do Mercado Pago |
| `message_failed` (`receive_count`), `message_malformed`, `payload_malformed`, `message_duplicate_skipped` | consumo de `billing-commands` |
| `outbox_publish_failed`, `outbox_message_stuck` | publicação no SNS |
| `circuit_breaker_state_changed`, `upstream_call_retry` (`upstream=mercadopago`) | chamadas ao Mercado Pago |

Alertas que cobrem o serviço (cada um com runbook em [`docs/runbooks`](https://github.com/Kc1t/postech-tc4-infra-k8s/tree/main/docs/runbooks) do infra-k8s):

| Alerta | Condição |
|---|---|
| `billing-service: taxa de erro acima de 5% em 5 min` | respostas 5xx sobre as requisições de negócio |
| `billing-service: latência p95 acima de 2 s em 5 min` | limite maior porque a rota de pagamento chama o Mercado Pago |
| `mensageria: mensagem falhou na última entrega antes da DLQ (receive_count >= 5)` | `message_failed` do consumidor `billing-commands` na 5ª entrega; o alarme `postech-tc4-prod-dlq-billing-commands` do CloudWatch confirma a DLQ |
| `mensageria: mensagem malformada recebida em 5 min` | `message_malformed` |
| `mensageria: mensagem travada no outbox` | `outbox_message_stuck` |
| SLO `billing-service: disponibilidade (sem 5xx)` | 99,5% em 7 dias |

## Configuração

Todas as variáveis estão em [`.env.example`](.env.example). Em desenvolvimento, `.env` é lido automaticamente. Na subida, `Config.Validate` recusa combinações inválidas e mostra todas de uma vez.

| Variável | Padrão | Descrição |
|---|---|---|
| `APP_PORT` / `APP_ENV` / `LOG_LEVEL` | `8082` / `development` / `info` | `APP_ENV=prod` exige `JWT_SECRET` próprio e mensageria configurada |
| `POSTGRES_DSN` | `postgres://postgres:postgres@localhost:5432/billing?sslmode=disable` | banco do serviço (no EKS, lido do Secrets Manager) |
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` / `DB_CONN_MAX_LIFETIME` | `10` / `5` / `30m` | pool de conexões |
| `JWT_SECRET` | `change-me-in-production` | segredo HS256 compartilhado com o os-service e a lambda-auth |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:3000` | allowlist separada por vírgula (`*` é recusado) |
| `AWS_REGION` / `AWS_ENDPOINT_URL` | — | região e endpoint do LocalStack em desenvolvimento (vazio na AWS) |
| `SQS_BILLING_COMMANDS_URL` / `SNS_BILLING_EVENTS_ARN` | — | sem os dois, a mensageria fica desligada e só a API HTTP sobe |
| `OUTBOX_POLL_INTERVAL` | `500ms` | intervalo do relay do outbox |
| `FAULT_INJECTION_ENABLED` | `false` | liga o `simulate_failure` dos comandos |
| `PAYMENT_GATEWAY` | `fake` | `mercadopago` ou `fake` |
| `MP_ACCESS_TOKEN` / `MP_WEBHOOK_SECRET` / `MP_NOTIFICATION_URL` | — | obrigatórios com `mercadopago` (credenciais de teste) |
| `MP_BASE_URL` / `MP_TIMEOUT` / `MP_PREFERENCE_TTL` / `MP_WEBHOOK_MAX_AGE` | `https://api.mercadopago.com` / `5s` / `24h` / `0` | ajustes do adapter |
| `FAKE_CHECKOUT_BASE_URL` | `http://localhost:8082` | base do link do gateway fake |
| `NEW_RELIC_APP_NAME` / `NEW_RELIC_LICENSE_KEY` | `billing-service` / — | sem licença, o agente fica desligado e o serviço funciona |
| `READINESS_TIMEOUT` / `SHUTDOWN_TIMEOUT` | `2s` / `20s` | timeout das checagens e do desligamento gracioso |

No EKS, o ConfigMap usa `NEW_RELIC_APP_NAME=billing-service-postech`, `MP_NOTIFICATION_URL=<gateway>/api/v1/webhooks/mercadopago` e `FAKE_CHECKOUT_BASE_URL=<gateway>`.

## Como rodar localmente

### Docker Compose (serviço + Postgres + LocalStack)

```bash
make up                      # docker compose up --build -d
make seed                    # opcional: orçamentos de demonstração
curl localhost:8082/health/ready
make logs                    # acompanha o serviço
make down
```

O LocalStack sobe com `deploy/localstack/init-aws.sh`, a mesma topologia de filas, tópicos e assinaturas do Terraform. As portas do host podem ser trocadas com `BILLING_SERVICE_PORT` (8082), `BILLING_POSTGRES_PORT` (5434) e `LOCALSTACK_PORT` (4566).

### Local (go run)

```bash
cp .env.example .env         # aponta para o Postgres (5434) e o LocalStack (4566) do compose
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

Para simular a saga sem o os-service, publique um comando na fila (os ids vêm do exemplo do JSON Schema `billing.CreateBudget`):

```bash
aws --endpoint-url http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/billing-commands \
  --message-body '{"message_id":"'$(uuidgen | tr A-Z a-z)'","type":"billing.CreateBudget","version":1,"saga_id":"f5517186-5fc2-49f0-9afb-21c5118ea375","os_id":"ee820080-21f1-41f8-857c-9302344afe32","producer":"os-service","occurred_at":"2026-11-01T12:00:00Z","payload":{"os_code":1042,"requester":{"id":"a0fd317e-365a-43b1-9362-328f0ca321a4","name":"Ana Souza","email":"ana.souza@example.com","document":"52998224725"},"items":[{"kind":"service","ref":"4012","description":"Troca de óleo","quantity":1,"unit_price":120}],"total":120}}'
```

O orçamento criado aparece em `GET /api/v1/budgets/os/ee820080-21f1-41f8-857c-9302344afe32`, e o evento `billing.BudgetCreated` vai para o tópico `billing-events`.

O fluxo completo, com os três serviços, as Lambdas no LocalStack e o Mailpit, roda no ambiente e2e do [postech-tc4-infra-k8s/local](https://github.com/Kc1t/postech-tc4-infra-k8s/tree/main/local) (`make all`).

## Testes e evidência de cobertura

| Comando | O que roda |
|---|---|
| `make test-infra` | sobe Postgres (porta `55433`) e LocalStack (porta `4567`) descartáveis |
| `make test` | todos os testes (`go test -p 1 ./...`), incluindo repositórios contra o Postgres de teste |
| `make test-unit` | só os pacotes que não usam banco (dispensa Docker) |
| `make test-component` | teste de componente (`-tags component`) com Postgres + LocalStack |
| `make cover` | cobertura sem `cmd/`, `docs/`, `mocks/` e `test/`, falhando abaixo de 80% |
| `make lint` | `golangci-lint` v2 (`.golangci.yml`) |
| `make test-infra-down` | remove os containers de teste |

Variáveis dos testes de banco: `TEST_DB_HOST`, `TEST_DB_PORT`, `TEST_DB_USER`, `TEST_DB_PASSWORD` e `TEST_DB_NAME`; o Makefile já usa `localhost:55433`, `postgres/postgres` e `billing_test`. O `-p 1` serializa os pacotes porque os testes de repositório, outbox e inbox dividem o mesmo banco.

- **Unidade**: domínio (máquinas de estado, dinheiro, CPF), casos de uso com gomock, handlers com `httptest`, middlewares (JWT com `alg=none`, HS512 e token expirado, CORS e correlação) e configuração.
- **Integração**: repositórios, unit of work, outbox e inbox contra Postgres real. Adapter do Mercado Pago contra `httptest`, com um vetor HMAC conhecido.
- **Contrato**: todo evento que os casos de uso gravam no outbox passa no JSON Schema do seu tipo, e o exemplo do schema de cada comando de `billing-commands` é aceito pelo handler registrado no consumidor (`internal/adapters/outbound/postgresql/contract_test.go`).
- **Componente** (`test/component`): sobe o serviço inteiro em processo; cada execução cria banco, fila, tópico e fila observadora únicos. Cenários: caminho feliz com o gateway fake (`CreateBudget → BudgetCreated → aprovação HTTP → RequestPayment → PaymentLinkCreated → simulate → PaymentApproved → RefundPayment → PaymentRefunded`), reentrega e idempotência por `os_id`, recusa, cancelamento com pagamento pendente, falhas simuladas e o adapter real do Mercado Pago contra uma API fake com webhook assinado e estorno com idempotency key.
- **BDD**: o fluxo completo da saga em Gherkin (godog) fica no os-service, que orquestra a saga ([`features/saga.feature`](https://github.com/Kc1t/postech-tc4-os-service/blob/main/features/saga.feature)); o lado do billing desse fluxo é coberto pelo teste de componente acima e pelo e2e do infra-k8s.

Cobertura por pacote (`make cover`, mesma medição do CI):

| Pacote | Cobertura |
|---|---|
| domínio (`internal/domain/entities`) | 99,4% |
| casos de uso `budget` / `payment` | 97,4% / 96,6% |
| adapters HTTP, mensageria, gateway fake e `config` | 100% |
| adapter Mercado Pago | 97,4% |
| adapter Postgres (repositórios, unit of work, migrations) | 92,5% |
| chassis `pkg/` | `contracts` 93,5%, `httpclient` 94,9%, `inbox` 93,3%, `messaging` 94,2%, `outbox` 88% a 92%, `awsx` e `logger` 80%, `env` e `observability` 100% |

O total varia um ou dois décimos entre execuções porque parte dos ramos do `pkg/outbox` depende do tempo do relay.

**Evidência de cobertura:** **95,8%** no `go test` (`make cover`) e **96,5%** no SonarQube, com o quality gate aprovado (0 bugs, 0 vulnerabilidades, 0 code smells, 0% de duplicação). Cada execução do [CI](https://github.com/Kc1t/postech-tc4-billing-service/actions/workflows/ci.yml) publica no resumo do job a cobertura (job `tests`) e a tabela do SonarQube (job `sonarqube`); o pipeline reprova abaixo de 80%.

## CI/CD e deploy

Pipeline próprio em [`.github/workflows/ci.yml`](.github/workflows/ci.yml), independente dos outros serviços, em PR e push para `main` e `homolog`.

| Job | O que faz |
|---|---|
| `quality` | `go mod tidy` sem diff, gofmt, `go vet` (também com a tag `component`), golangci-lint, gosec (falha a partir de severidade média), govulncheck e Swagger em dia com o código |
| `tests` | Postgres e LocalStack de teste, `make cover` e cobertura mínima de 80% |
| `component` | teste de componente com Postgres e LocalStack (`make test-component`) |
| `sonarqube` | SonarQube Community num container do job, com quality gate próprio: cobertura ≥ 80%, duplicação ≤ 3% e nota A em confiabilidade, segurança e manutenibilidade |
| `terraform` | fmt, validate e tfsec do `infra/` |
| `image` | build da imagem e Trivy (CRITICAL com correção bloqueia) |
| `deploy` | só em push na `main`, depois de todos os anteriores e com `AWS_DEPLOY_ENABLED=true` |

A `main` é protegida: só recebe PR, com a branch atualizada, conversas resolvidas e os checks `quality`, `tests`, `component`, `sonarqube`, `terraform` e `image` obrigatórios, inclusive para administradores. O deploy depende da variável `AWS_DEPLOY_ENABLED=true` porque as credenciais do Learner Lab expiram a cada sessão.

O job `deploy`:

1. `terraform apply` do `infra/` (RDS, parâmetros, security group e segredo da DSN);
2. lê a DSN no Secrets Manager;
3. build e push da imagem no ECR (`billing-service:<sha>`);
4. cria o `Secret` `billing-service-secret` (`JWT_SECRET`, `POSTGRES_DSN`, `MP_ACCESS_TOKEN`, `MP_WEBHOOK_SECRET`, `NEW_RELIC_LICENSE_KEY`);
5. troca os `*_PLACEHOLDER` dos manifests (imagem, namespace, região, conta, CORS, `FAULT_INJECTION_ENABLED`, endpoint do API Gateway e `PAYMENT_GATEWAY`) e falha se sobrar algum;
6. `kubectl apply` no namespace `postech`, `rollout status` e smoke test em `/health/ready` pelo NLB.

Depois do primeiro deploy, o workflow do [postech-tc4-infra-k8s](https://github.com/Kc1t/postech-tc4-infra-k8s) liga as rotas do API Gateway ao NLB do serviço.

### Imagem e manifestos Kubernetes

[`Dockerfile`](Dockerfile) multi-stage: binário estático compilado em `golang:1.26-alpine` e executado em `alpine:3.22` com o usuário 10001 e `HEALTHCHECK` em `/health/live`. Os manifestos ficam em [`k8s/`](k8s/) e não declaram namespace.

| Arquivo | Conteúdo |
|---|---|
| `deployment.yaml` | 2 réplicas, `RollingUpdate` com `maxSurge: 1` e `maxUnavailable: 0`, probes separadas (`startupProbe` e `livenessProbe` em `/health/live`, `readinessProbe` em `/health/ready`), requests de 100m/128Mi e limits de 500m/256Mi, `runAsNonRoot` (UID 10001), `readOnlyRootFilesystem`, sem capabilities e `preStop` de 5 s |
| `service.yaml` | `ClusterIP` `billing-service` (API composition do os-service) e NLB `billing-service-public` (rotas `/api/v1/budgets`, `/api/v1/payments`, `/api/v1/webhooks/mercadopago` e `/fake-checkout` do API Gateway) |
| `configmap.yaml` / `secret.yaml` | configuração não sensível e modelo do Secret; o `secret.yaml` não é aplicado, o CI cria o Secret |
| `hpa.yaml` / `pdb.yaml` | HPA de 2 a 6 réplicas (CPU 70%, memória 80%) e PDB `minAvailable: 1` |
| `networkpolicy.yaml` | entrada só na porta 8082; saída só para DNS, Postgres (5432) e HTTPS (443) |

No `SIGTERM`, o serviço para de aceitar conexões, termina as requisições em andamento e depois para o consumidor e o relay. Mensagem em processamento termina ou volta para a fila pela visibilidade, e o que ficou no outbox é publicado por outra réplica ou na próxima subida.

## Decisões e limitações

- **Porta do gateway**: `CreateCheckout` recebe um `CheckoutRequest` (ids, código da OS e valor) em vez da entidade, para o adapter não depender do domínio. `GetPayment` devolve `ProviderPayment`, que inclui `status_detail` para o motivo da falha.
- **Chamada ao gateway dentro da transação do inbox**: a preferência é criada antes do commit. Se o commit falhar, a reentrega cria outra preferência, o que é inofensivo porque a antiga nunca é divulgada. Em troca, nunca existe pagamento gravado sem link.
- **Webhook síncrono**: valida, relê o pagamento e aplica o resultado na própria requisição. Falha transitória devolve 503 e o Mercado Pago reenvia.
- **401/403 do Mercado Pago contam como transitórios**: token rotacionado ou expirado não deve virar `PaymentFailed` definitivo. Um estorno recusado de forma definitiva vai para a DLQ para tratamento manual, porque não existe compensação da compensação.
- **Pagamento órfão**: um `approved` que chega para uma tentativa cancelada ou falha é estornado automaticamente (idempotency key `orphan-{id}`).
- **Rejeição definitiva**: o Mercado Pago permite tentar de novo depois de um `rejected`, mas para a saga o `rejected` encerra o pagamento (`PaymentFailed`).
- **Chassis copiado**: o `pkg/` é copiado do os-service trocando só o import path e passa pelo mesmo lint; o `make contracts` do e2e falha se o `pkg/contracts` divergir entre os repositórios.
- **Mercado Pago nos testes**: os testes automatizados do adapter rodam contra uma API fake (`httptest`) com os mesmos endpoints e um vetor HMAC conhecido; o CI não chama o sandbox, porque exigiria credenciais no runner. O deploy usa o gateway fake por padrão e passa ao Mercado Pago com `scripts/setup-github.sh mercadopago` (credenciais de teste e `PAYMENT_GATEWAY=mercadopago`).
- **Janela anti-replay desligada por padrão** (`MP_WEBHOOK_MAX_AGE=0`), para evitar recusas por diferença de relógio; a assinatura continua obrigatória.
- **Orçamento não expira sozinho**: o prazo de aprovação é do orquestrador (`SAGA_APPROVAL_TIMEOUT` no os-service), que envia `CancelBudget` quando vence.
