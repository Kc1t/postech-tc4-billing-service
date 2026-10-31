// Package config lê a configuração do billing-service das variáveis de ambiente (com .env opcional
// em desenvolvimento) e valida as combinações que impediriam o serviço de funcionar.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/pkg/env"
	"github.com/joho/godotenv"
)

type AppEnv string

const (
	EnvDevelopment AppEnv = "development"
	EnvProduction  AppEnv = "prod"
)

// Gateways de pagamento suportados (PAYMENT_GATEWAY).
const (
	GatewayMercadoPago = "mercadopago"
	GatewayFake        = "fake"
)

// DefaultJWTSecret é o segredo de desenvolvimento; em produção Validate exige outro valor.
const DefaultJWTSecret = "change-me-in-production"

type Config struct {
	AppPort            string
	AppEnv             AppEnv
	LogLevel           string
	NewRelicAppName    string
	NewRelicLicenseKey string

	PostgresDSN       string
	DBMaxOpenConns    int
	DBMaxIdleConns    int
	DBConnMaxLifetime time.Duration

	JWTSecret string
	// CORSAllowedOrigins é a allowlist de origens do navegador (nunca "*").
	CORSAllowedOrigins []string

	// Mensageria (SNS/SQS). Sem fila e tópico, o serviço sobe só com a API HTTP.
	BillingCommandsQueueURL string
	BillingEventsTopicARN   string
	OutboxPollInterval      time.Duration

	FaultInjectionEnabled bool

	// Gateway de pagamento.
	PaymentGateway      string
	MPAccessToken       string
	MPWebhookSecret     string
	MPNotificationURL   string
	MPBaseURL           string
	MPTimeout           time.Duration
	MPPreferenceTTL     time.Duration
	MPWebhookMaxAge     time.Duration
	FakeCheckoutBaseURL string

	ReadinessTimeout time.Duration
	ShutdownTimeout  time.Duration
}

// MessagingEnabled indica se o consumidor de billing-commands e o relay do outbox devem rodar.
func (c *Config) MessagingEnabled() bool {
	return c.BillingCommandsQueueURL != "" && c.BillingEventsTopicARN != ""
}

// FakeGateway indica se o gateway fake está ativo (habilita /fake-checkout e o simulate).
func (c *Config) FakeGateway() bool { return c.PaymentGateway == GatewayFake }

func Load() *Config {
	_ = godotenv.Load()

	return &Config{
		AppPort:            env.GetOrDefault("APP_PORT", "8082"),
		AppEnv:             AppEnv(env.GetOrDefault("APP_ENV", string(EnvDevelopment))),
		LogLevel:           env.GetOrDefault("LOG_LEVEL", "info"),
		NewRelicAppName:    env.GetOrDefault("NEW_RELIC_APP_NAME", "billing-service"),
		NewRelicLicenseKey: env.GetOrDefault("NEW_RELIC_LICENSE_KEY", ""),

		PostgresDSN:       env.GetOrDefault("POSTGRES_DSN", "postgres://postgres:postgres@localhost:5432/billing?sslmode=disable"),
		DBMaxOpenConns:    env.GetIntOrDefault("DB_MAX_OPEN_CONNS", 10),
		DBMaxIdleConns:    env.GetIntOrDefault("DB_MAX_IDLE_CONNS", 5),
		DBConnMaxLifetime: env.GetDurationOrDefault("DB_CONN_MAX_LIFETIME", 30*time.Minute),

		JWTSecret:          env.GetOrDefault("JWT_SECRET", DefaultJWTSecret),
		CORSAllowedOrigins: env.GetListOrDefault("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000"}),

		BillingCommandsQueueURL: env.GetOrDefault("SQS_BILLING_COMMANDS_URL", ""),
		BillingEventsTopicARN:   env.GetOrDefault("SNS_BILLING_EVENTS_ARN", ""),
		OutboxPollInterval:      env.GetDurationOrDefault("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),

		FaultInjectionEnabled: env.GetBoolOrDefault("FAULT_INJECTION_ENABLED", false),

		PaymentGateway:      strings.ToLower(env.GetOrDefault("PAYMENT_GATEWAY", GatewayFake)),
		MPAccessToken:       env.GetOrDefault("MP_ACCESS_TOKEN", ""),
		MPWebhookSecret:     env.GetOrDefault("MP_WEBHOOK_SECRET", ""),
		MPNotificationURL:   env.GetOrDefault("MP_NOTIFICATION_URL", ""),
		MPBaseURL:           env.GetOrDefault("MP_BASE_URL", "https://api.mercadopago.com"),
		MPTimeout:           env.GetDurationOrDefault("MP_TIMEOUT", 5*time.Second),
		MPPreferenceTTL:     env.GetDurationOrDefault("MP_PREFERENCE_TTL", 24*time.Hour),
		MPWebhookMaxAge:     env.GetDurationOrDefault("MP_WEBHOOK_MAX_AGE", 0),
		FakeCheckoutBaseURL: env.GetOrDefault("FAKE_CHECKOUT_BASE_URL", "http://localhost:8082"),

		ReadinessTimeout: env.GetDurationOrDefault("READINESS_TIMEOUT", 2*time.Second),
		ShutdownTimeout:  env.GetDurationOrDefault("SHUTDOWN_TIMEOUT", 20*time.Second),
	}
}

// Validate devolve todos os problemas de configuração de uma vez, para o pod falhar na subida
// (CrashLoopBackOff visível) em vez de quebrar na primeira requisição.
func (c *Config) Validate() error {
	var errs []error
	switch c.PaymentGateway {
	case GatewayFake:
	case GatewayMercadoPago:
		for name, v := range map[string]string{
			"MP_ACCESS_TOKEN":     c.MPAccessToken,
			"MP_WEBHOOK_SECRET":   c.MPWebhookSecret,
			"MP_NOTIFICATION_URL": c.MPNotificationURL,
		} {
			if v == "" {
				errs = append(errs, fmt.Errorf("%s é obrigatório com PAYMENT_GATEWAY=mercadopago", name))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("PAYMENT_GATEWAY inválido %q (use mercadopago ou fake)", c.PaymentGateway))
	}
	for _, o := range c.CORSAllowedOrigins {
		if strings.Contains(o, "*") {
			errs = append(errs, errors.New("CORS_ALLOWED_ORIGINS não aceita curinga: liste as origens"))
			break
		}
	}
	if c.JWTSecret == "" {
		errs = append(errs, errors.New("JWT_SECRET é obrigatório"))
	}
	if c.AppEnv == EnvProduction {
		if c.JWTSecret == DefaultJWTSecret {
			errs = append(errs, errors.New("JWT_SECRET padrão não pode ser usado em produção"))
		}
		if !c.MessagingEnabled() {
			errs = append(errs, errors.New("SQS_BILLING_COMMANDS_URL e SNS_BILLING_EVENTS_ARN são obrigatórios em produção"))
		}
	}
	return errors.Join(errs...)
}
