package config

import (
	"strings"
	"testing"
	"time"
)

var keys = []string{
	"APP_PORT", "APP_ENV", "POSTGRES_DSN", "JWT_SECRET", "CORS_ALLOWED_ORIGINS", "PAYMENT_GATEWAY",
	"SQS_BILLING_COMMANDS_URL", "SNS_BILLING_EVENTS_ARN", "FAULT_INJECTION_ENABLED",
	"MP_ACCESS_TOKEN", "MP_WEBHOOK_SECRET", "MP_NOTIFICATION_URL", "MP_TIMEOUT", "MP_PREFERENCE_TTL",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	cfg := Load()

	if cfg.AppPort != "8082" || cfg.AppEnv != EnvDevelopment || cfg.NewRelicAppName != "billing-service" {
		t.Errorf("app: %q %q %q", cfg.AppPort, cfg.AppEnv, cfg.NewRelicAppName)
	}
	if cfg.PaymentGateway != GatewayFake || !cfg.FakeGateway() || cfg.MPBaseURL != "https://api.mercadopago.com" {
		t.Errorf("gateway: %q %q", cfg.PaymentGateway, cfg.MPBaseURL)
	}
	if cfg.MPTimeout != 5*time.Second || cfg.MPPreferenceTTL != 24*time.Hour || cfg.MPWebhookMaxAge != 0 {
		t.Errorf("tempos do MP: %v %v %v", cfg.MPTimeout, cfg.MPPreferenceTTL, cfg.MPWebhookMaxAge)
	}
	if cfg.MessagingEnabled() || cfg.FaultInjectionEnabled {
		t.Error("sem fila/tópico a mensageria fica desligada e fault injection vem desligado")
	}
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.PostgresDSN == "" {
		t.Errorf("cors/dsn: %v %q", cfg.CORSAllowedOrigins, cfg.PostgresDSN)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("os padrões de desenvolvimento devem ser válidos: %v", err)
	}
}

func TestLoad_Override(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_PORT", "9090")
	t.Setenv("PAYMENT_GATEWAY", "MercadoPago")
	t.Setenv("SQS_BILLING_COMMANDS_URL", "http://q")
	t.Setenv("SNS_BILLING_EVENTS_ARN", "arn:t")
	t.Setenv("FAULT_INJECTION_ENABLED", "true")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.com, https://b.com")
	t.Setenv("MP_TIMEOUT", "3s")

	cfg := Load()
	if cfg.AppPort != "9090" || cfg.PaymentGateway != GatewayMercadoPago || cfg.FakeGateway() {
		t.Errorf("override: %q %q", cfg.AppPort, cfg.PaymentGateway)
	}
	if !cfg.MessagingEnabled() || !cfg.FaultInjectionEnabled || len(cfg.CORSAllowedOrigins) != 2 || cfg.MPTimeout != 3*time.Second {
		t.Errorf("override: %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	valid := func() *Config {
		return &Config{
			AppEnv: EnvProduction, JWTSecret: "s3cr3t", PaymentGateway: GatewayMercadoPago,
			MPAccessToken: "APP_USR-1", MPWebhookSecret: "w", MPNotificationURL: "https://x/webhooks/mercadopago",
			BillingCommandsQueueURL: "http://q", BillingEventsTopicARN: "arn:t",
			CORSAllowedOrigins: []string{"https://app.example.com"},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("configuração de produção completa: %v", err)
	}

	cases := map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"gateway desconhecido":   {func(c *Config) { c.PaymentGateway = "stripe" }, "PAYMENT_GATEWAY inválido"},
		"sem token do MP":        {func(c *Config) { c.MPAccessToken = "" }, "MP_ACCESS_TOKEN"},
		"sem segredo do webhook": {func(c *Config) { c.MPWebhookSecret = "" }, "MP_WEBHOOK_SECRET"},
		"sem notification url":   {func(c *Config) { c.MPNotificationURL = "" }, "MP_NOTIFICATION_URL"},
		"cors curinga":           {func(c *Config) { c.CORSAllowedOrigins = []string{"*"} }, "curinga"},
		"jwt vazio":              {func(c *Config) { c.JWTSecret = "" }, "JWT_SECRET é obrigatório"},
		"jwt padrão em prod":     {func(c *Config) { c.JWTSecret = DefaultJWTSecret }, "JWT_SECRET padrão"},
		"prod sem mensageria":    {func(c *Config) { c.BillingEventsTopicARN = "" }, "SNS_BILLING_EVENTS_ARN"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("esperava erro com %q, veio %v", tc.want, err)
			}
		})
	}
}
