package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Kc1t/postech-tc4-billing-service/cmd/api/bootstrap"
	"github.com/Kc1t/postech-tc4-billing-service/config"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	"github.com/gin-gonic/gin"
)

// @title           Billing Service — Oficina Mecânica
// @version         1.0
// @description     Orçamento e pagamento da ordem de serviço. Participa da saga orquestrada pelo os-service:
// @description     consome comandos da fila billing-commands, publica eventos no tópico billing-events (via outbox)
// @description     e integra com o Mercado Pago (Checkout Pro + webhook assinado) ou com um gateway fake.
// @BasePath        /
// @securityDefinitions.apikey BearerAuth
// @in              header
// @name            Authorization
// @description     JWT HS256 no formato "Bearer {token}" (claims sub, role e, para clientes, document).
func main() {
	cfg := config.Load()
	log := logger.New(cfg.LogLevel)
	if cfg.AppEnv == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := bootstrap.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup_failed", "error", err)
		os.Exit(1)
	}
	defer app.Close()

	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", ":"+cfg.AppPort)
	if err != nil {
		log.Error("listen_failed", "error", err)
		app.Close()
		os.Exit(1) //nolint:gocritic // Close já foi chamado explicitamente
	}
	if err := app.Serve(ctx, ln); err != nil {
		log.Error("server_failed", "error", err)
		app.Close()
		os.Exit(1)
	}
}
