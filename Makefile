.PHONY: help run build up down logs seed swagger mocks test test-unit test-component cover lint tidy test-infra test-infra-down

APP_NAME=billing-service
MAIN=./cmd/api
COVER_PKGS=$$(go list ./... | grep -v -E '/cmd/|/docs|/mocks|/test/')
COVER_MIN=80
# Pacotes que não precisam de banco (sem Docker).
UNIT_PKGS=$$(go list ./... | grep -v -E '/outbound/postgresql$$|/pkg/outbox|/pkg/inbox')

# Postgres e LocalStack descartáveis dos testes (make test-infra).
TEST_DB_PORT?=55433
TEST_LOCALSTACK_PORT?=4567
TEST_ENV=TEST_DB_HOST=localhost TEST_DB_PORT=$(TEST_DB_PORT) TEST_DB_USER=postgres TEST_DB_PASSWORD=postgres TEST_DB_NAME=billing_test
COMPONENT_ENV=$(TEST_ENV) AWS_ENDPOINT_URL=http://localhost:$(TEST_LOCALSTACK_PORT) AWS_REGION=us-east-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test

help:
	@echo "run | build | up | down | logs | seed | swagger | mocks | test-infra | test | test-component | cover | lint | tidy"

run:
	go run $(MAIN)

build:
	CGO_ENABLED=0 go build -trimpath -o bin/$(APP_NAME) $(MAIN)

up:
	docker compose up --build -d

down:
	docker compose down

logs:
	docker compose logs -f billing-service

# Dados de demonstração (idempotente) no Postgres do docker compose.
seed:
	docker compose exec -T postgres psql -U postgres -d billing < scripts/seed.sql

swagger:
	swag init -g $(MAIN)/main.go -o docs --parseInternal

mocks:
	cd internal/ports && go generate ./...

test-infra:
	docker run -d --rm --name tc4-billing-pg -p $(TEST_DB_PORT):5432 -e POSTGRES_USER=postgres \
		-e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=billing_test postgres:16-alpine
	docker run -d --rm --name tc4-billing-localstack -p $(TEST_LOCALSTACK_PORT):4566 \
		-e SERVICES=sqs,sns localstack/localstack:3.8

test-infra-down:
	docker rm -f tc4-billing-pg tc4-billing-localstack

# -p 1: os testes de repositório de pacotes diferentes compartilham o mesmo banco.
test:
	$(TEST_ENV) go test -p 1 -count=1 ./...

test-unit:
	go test -count=1 $(UNIT_PKGS)

test-component:
	$(COMPONENT_ENV) go test -tags component -count=1 -v ./test/component/...

cover:
	$(TEST_ENV) go test -p 1 -count=1 -coverprofile=coverage.out $(COVER_PKGS)
	@go tool cover -func=coverage.out | tail -1
	@go tool cover -func=coverage.out | awk '/^total:/ { sub("%", "", $$3); if ($$3 + 0 < $(COVER_MIN)) { print "cobertura abaixo de $(COVER_MIN)%"; exit 1 } }'

lint:
	golangci-lint run ./...

tidy:
	go mod tidy
