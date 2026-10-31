// Package bootstrap faz a injeção de dependências do billing-service: abre o banco e aplica as
// migrations, escolhe o gateway de pagamento, monta casos de uso, handlers HTTP, consumidor da fila
// billing-commands e relay do outbox, e controla o ciclo de vida (subida e desligamento gracioso).
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/cmd/api/routes"
	"github.com/Kc1t/postech-tc4-billing-service/config"
	budgethandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/budget"
	healthhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/health"
	paymenthandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/payment"
	webhookhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/webhook"
	inboundmsg "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/messaging"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/fakegateway"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/mercadopago"
	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/postgresql"
	budgetuc "github.com/Kc1t/postech-tc4-billing-service/internal/application/usecase/budget"
	paymentuc "github.com/Kc1t/postech-tc4-billing-service/internal/application/usecase/payment"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/migrations"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/awsx"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/httpclient"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/messaging"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/observability"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/outbox"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/gin-gonic/gin"
	"github.com/newrelic/go-agent/v3/newrelic"
	"gorm.io/gorm"
)

const (
	// Producer identifica o serviço no campo producer dos envelopes publicados.
	Producer = "billing-service"
	// CommandsConsumer é o nome do consumidor (logs, transações e chave do inbox).
	CommandsConsumer = "billing-commands"
)

// App é o serviço montado.
type App struct {
	cfg      *config.Config
	log      *slog.Logger
	nr       *newrelic.Application
	db       *gorm.DB
	router   *gin.Engine
	consumer *messaging.Consumer
	relay    *outbox.Relay
}

// New monta o serviço. Falha se o banco estiver inacessível ou se as migrations não aplicarem.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuração inválida: %w", err)
	}
	a := &App{cfg: cfg, log: log}

	nr, err := observability.NewRelic(cfg.NewRelicAppName, cfg.NewRelicLicenseKey)
	if err != nil {
		log.Warn("newrelic_disabled", "error", err)
	}
	a.nr = nr

	db, err := postgresql.Open(cfg.PostgresDSN, postgresql.PoolConfig{
		MaxOpenConns: cfg.DBMaxOpenConns, MaxIdleConns: cfg.DBMaxIdleConns, ConnMaxLifetime: cfg.DBConnMaxLifetime,
	})
	if err != nil {
		return nil, err
	}
	a.db = db
	applied, err := postgresql.Migrate(ctx, db, migrations.FS)
	if err != nil {
		a.Close()
		return nil, err
	}
	log.Info("migrations_applied", "applied", applied)

	gateway, verifier := a.paymentGateway()
	uow := postgresql.NewUnitOfWork(db, cfg.BillingEventsTopicARN, Producer)
	budgets := postgresql.NewBudgetRepository(db)
	payments := postgresql.NewPaymentRepository(db)

	checks := map[string]healthhandler.Check{"database": func(ctx context.Context) error { return postgresql.Ping(ctx, db) }}

	if cfg.MessagingEnabled() {
		awsCfg, err := awsx.LoadConfig(ctx)
		if err != nil {
			a.Close()
			return nil, fmt.Errorf("aws config: %w", err)
		}
		sqsClient := sqs.NewFromConfig(awsCfg)
		snsClient := sns.NewFromConfig(awsCfg)

		a.relay = outbox.NewRelay(db, messaging.NewAWSPublisher(snsClient, sqsClient), nr, log).WithInterval(cfg.OutboxPollInterval)
		a.consumer = messaging.NewConsumer(sqsClient, cfg.BillingCommandsQueueURL, CommandsConsumer, nr, log)
		inboundmsg.NewCommandHandlers(
			postgresql.NewInbox(db, CommandsConsumer),
			budgetuc.NewCreateBudget(uow, cfg.FaultInjectionEnabled),
			paymentuc.NewRequestPayment(uow, gateway, cfg.FaultInjectionEnabled),
			budgetuc.NewCancelBudget(uow, gateway),
			paymentuc.NewRefundPayment(uow, gateway),
		).Register(a.consumer)

		checks["sqs"] = func(ctx context.Context) error {
			_, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
				QueueUrl:       aws.String(cfg.BillingCommandsQueueURL),
				AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
			})
			return err
		}
	} else {
		log.Warn("messaging_disabled", "hint", "defina SQS_BILLING_COMMANDS_URL e SNS_BILLING_EVENTS_ARN")
	}

	a.router = routes.New(routes.Options{
		Logger:         log,
		NewRelic:       nr,
		JWTSecret:      cfg.JWTSecret,
		AllowedOrigins: cfg.CORSAllowedOrigins,
		FakeCheckout:   cfg.FakeGateway(),
		Health:         healthhandler.NewHandler(checks, cfg.ReadinessTimeout),
		Budget: budgethandler.NewHandler(
			budgetuc.NewGetBudgetByID(budgets, payments),
			budgetuc.NewGetBudgetByOS(budgets, payments),
			budgetuc.NewApproveBudget(uow),
			budgetuc.NewRejectBudget(uow),
		),
		Payment: paymenthandler.NewHandler(
			paymentuc.NewGetPayment(payments, budgets),
			paymentuc.NewSimulatePayment(uow, payments, gateway),
		),
		Webhook: webhookhandler.NewHandler(verifier, paymentuc.NewProcessPaymentNotification(uow, gateway)),
	})
	return a, nil
}

// paymentGateway escolhe o adapter conforme PAYMENT_GATEWAY. Com o fake, o verificador do webhook
// fica sem segredo e recusa toda notificação (falha fechada).
func (a *App) paymentGateway() (ports.PaymentGateway, ports.WebhookVerifier) {
	verifier := mercadopago.NewSignatureVerifier(a.cfg.MPWebhookSecret, a.cfg.MPWebhookMaxAge)
	if a.cfg.FakeGateway() {
		a.log.Warn("payment_gateway_fake", "checkout_base_url", a.cfg.FakeCheckoutBaseURL)
		return fakegateway.New(a.cfg.FakeCheckoutBaseURL), verifier
	}
	client := httpclient.New(httpclient.Config{Name: "mercadopago", Timeout: a.cfg.MPTimeout}, a.log)
	return mercadopago.New(mercadopago.Config{
		BaseURL:         a.cfg.MPBaseURL,
		AccessToken:     a.cfg.MPAccessToken,
		NotificationURL: a.cfg.MPNotificationURL,
		PreferenceTTL:   a.cfg.MPPreferenceTTL,
	}, client), verifier
}

// Handler devolve o roteador HTTP.
func (a *App) Handler() http.Handler { return a.router }

// Serve atende HTTP no listener e roda os workers (consumidor e relay) até ctx ser cancelado.
// No desligamento: para de aceitar conexões e termina as requisições em andamento, depois para
// consumidor e relay. Mensagem em processamento termina ou volta para a fila pela visibilidade;
// o que ficar no outbox é publicado por outra réplica ou na próxima subida — nada se perde.
func (a *App) Serve(ctx context.Context, ln net.Listener) error {
	workersCtx, stopWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer stopWorkers()
	var workers sync.WaitGroup
	start := func(name string, run func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			run(workersCtx)
			a.log.Info("worker_stopped", "worker", name)
		}()
	}
	if a.relay != nil {
		start("outbox-relay", a.relay.Run)
		start(CommandsConsumer, a.consumer.Run)
	}

	srv := &http.Server{Handler: a.router, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	a.log.Info("server_listening", "addr", ln.Addr().String(), "env", string(a.cfg.AppEnv),
		"payment_gateway", a.cfg.PaymentGateway, "messaging", a.cfg.MessagingEnabled(),
		"fault_injection", a.cfg.FaultInjectionEnabled)

	var err error
	select {
	case <-ctx.Done():
	case err = <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}

	a.log.Info("shutdown_started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()
	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		a.log.Error("http_shutdown_failed", "error", shutdownErr)
	}
	stopWorkers()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		a.log.Warn("workers_shutdown_timeout")
	}
	a.log.Info("shutdown_completed")
	return err
}

// Close libera conexões do banco e encerra o agente do New Relic.
func (a *App) Close() {
	if a.db != nil {
		if sqlDB, err := a.db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
	if a.nr != nil {
		a.nr.Shutdown(5 * time.Second)
	}
}
