// Package routes monta o roteador HTTP do billing-service: middlewares, health, Swagger, rotas da
// API (/api/v1) com as regras de papel e, com o gateway fake, a página de checkout local.
package routes

import (
	"log/slog"
	"net/http"

	_ "github.com/Kc1t/postech-tc4-billing-service/docs" // registra a especificação gerada pelo swag

	"github.com/Kc1t/postech-tc4-billing-service/cmd/api/middleware"
	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	budgethandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/budget"
	healthhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/health"
	paymenthandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/payment"
	webhookhandler "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http/webhook"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/gin-gonic/gin"
	"github.com/newrelic/go-agent/v3/integrations/nrgin"
	"github.com/newrelic/go-agent/v3/newrelic"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// HealthPaths ficam fora do log de requisições (as probes rodam a cada poucos segundos).
var HealthPaths = []string{"/health/live", "/health/ready"}

// Options são as dependências do roteador.
type Options struct {
	Logger         *slog.Logger
	NewRelic       *newrelic.Application // nil: agente desligado
	JWTSecret      string
	AllowedOrigins []string
	// FakeCheckout publica GET /fake-checkout/{payment_id} (só com PAYMENT_GATEWAY=fake).
	FakeCheckout bool

	Health  *healthhandler.Handler
	Budget  *budgethandler.Handler
	Payment *paymenthandler.Handler
	Webhook *webhookhandler.Handler
}

// New devolve o roteador pronto.
func New(o Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	if o.NewRelic != nil {
		r.Use(nrgin.Middleware(o.NewRelic), middleware.NewRelicContext())
	}
	r.Use(middleware.CorrelationID(), middleware.RequestLogger(o.Logger, HealthPaths...), middleware.CORS(o.AllowedOrigins))

	r.NoRoute(func(c *gin.Context) {
		httputil.Abort(c, http.StatusNotFound, httputil.CodeNotFound, "route not found")
	})

	r.GET("/health/live", o.Health.Live)
	r.GET("/health/ready", o.Health.Ready)
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	if o.FakeCheckout {
		r.GET("/fake-checkout/:payment_id", o.Payment.FakeCheckout)
	}

	api := r.Group("/api/v1")
	// Público: a autenticidade vem da assinatura x-signature, validada no handler.
	api.POST("/webhooks/mercadopago", o.Webhook.MercadoPago)

	auth := api.Group("", middleware.Auth(o.JWTSecret))
	readers := middleware.RequireRole(entities.RoleAdmin, entities.RoleClient)
	clients := middleware.RequireRole(entities.RoleClient)
	admins := middleware.RequireRole(entities.RoleAdmin)
	// Depois do papel: id de orçamento ou de pagamento que não é UUID responde 400, sem ir ao banco.
	id := httputil.UUIDParams("id")

	auth.GET("/budgets/os/:os_id", readers, o.Budget.GetByOS)
	auth.GET("/budgets/:id", readers, id, o.Budget.GetByID)
	auth.POST("/budgets/:id/approve", clients, id, o.Budget.Approve)
	auth.POST("/budgets/:id/reject", clients, id, o.Budget.Reject)
	auth.GET("/payments/:id", readers, id, o.Payment.Get)
	auth.POST("/payments/:id/simulate", admins, id, o.Payment.Simulate)
	return r
}
