package middleware

import (
	"log/slog"
	"time"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	"github.com/gin-gonic/gin"
)

// RequestLogger coloca no ctx da requisição um logger com correlation_id, método e rota (os
// casos de uso o recuperam com logger.FromContext) e registra uma linha por requisição, com nível
// pelo status. Os caminhos em skipPaths (probes de health) não geram linha de acesso.
func RequestLogger(base *slog.Logger, skipPaths ...string) gin.HandlerFunc {
	skip := make(map[string]bool, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = true
	}
	return func(c *gin.Context) {
		reqLog := base.With(
			slog.String(httputil.KeyCorrelationID, httputil.CorrelationID(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
		c.Request = c.Request.WithContext(logger.ContextWith(c.Request.Context(), reqLog))

		start := time.Now()
		c.Next()
		if skip[c.Request.URL.Path] {
			return
		}

		attrs := []any{
			slog.Int("status", c.Writer.Status()),
			slog.Int64("latency_ms", time.Since(start).Milliseconds()),
			slog.String("client_ip", c.ClientIP()),
		}
		if route := c.FullPath(); route != "" {
			attrs = append(attrs, slog.String("route", route))
		}
		if userID := c.GetString(httputil.KeyUserID); userID != "" {
			attrs = append(attrs, slog.String(httputil.KeyUserID, userID))
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}
		l := logger.WithTrace(c.Request.Context(), reqLog)
		switch status := c.Writer.Status(); {
		case status >= 500:
			l.Error("request", attrs...)
		case status >= 400:
			l.Warn("request", attrs...)
		default:
			l.Info("request", attrs...)
		}
	}
}
