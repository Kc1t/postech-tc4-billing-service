package middleware

import (
	"net/http"
	"strings"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/gin-gonic/gin"
)

// CORS responde só para as origens da allowlist (CORS_ALLOWED_ORIGINS); nunca devolve "*".
//
//   - Origem permitida: recebe os headers CORS; o preflight (OPTIONS) termina em 204.
//   - Origem fora da lista: requisição simples segue sem headers CORS (o navegador bloqueia a
//     leitura da resposta); o preflight é recusado com 403 ORIGIN_NOT_ALLOWED.
//   - Sem Origin (servidor a servidor, curl, webhook do Mercado Pago): segue normalmente.
func CORS(allowed []string) gin.HandlerFunc {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" && o != "*" {
			set[o] = true
		}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		allowedOrigin := origin != "" && set[origin]
		if allowedOrigin {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, "+CorrelationHeader)
			h.Set("Access-Control-Expose-Headers", CorrelationHeader)
			h.Set("Access-Control-Max-Age", "600")
		}
		if c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != "" {
			if origin != "" && !allowedOrigin {
				httputil.Abort(c, http.StatusForbidden, httputil.CodeOriginNotAllowed, "origin not allowed")
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
