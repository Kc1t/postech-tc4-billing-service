// Package healthhandler expõe as probes do Kubernetes: liveness (o processo responde) e
// readiness (as dependências respondem — sem banco ou sem AWS o pod sai do Service, mas não é
// reiniciado).
package healthhandler

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// Check verifica uma dependência.
type Check func(ctx context.Context) error

// Handler responde as probes.
type Handler struct {
	checks  map[string]Check
	timeout time.Duration
}

// NewHandler cria o handler. checks é nome → verificação (ex.: "database", "aws").
func NewHandler(checks map[string]Check, timeout time.Duration) *Handler {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Handler{checks: checks, timeout: timeout}
}

// Status é a resposta das probes.
type Status struct {
	Status string            `json:"status" example:"ok" enums:"ok,unavailable"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Live godoc
// @Summary  Liveness
// @Tags     health
// @Produce  json
// @Success  200 {object} Status
// @Router   /health/live [get]
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, Status{Status: "ok"})
}

// Ready godoc
// @Summary  Readiness (banco e AWS)
// @Tags     health
// @Produce  json
// @Success  200 {object} Status
// @Failure  503 {object} Status
// @Router   /health/ready [get]
func (h *Handler) Ready(c *gin.Context) {
	names := make([]string, 0, len(h.checks))
	for name := range h.checks {
		names = append(names, name)
	}
	sort.Strings(names)

	resp := Status{Status: "ok", Checks: make(map[string]string, len(names))}
	code := http.StatusOK
	for _, name := range names {
		ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
		err := h.checks[name](ctx)
		cancel()
		if err != nil {
			resp.Checks[name] = "error: " + err.Error()
			resp.Status = "unavailable"
			code = http.StatusServiceUnavailable
			continue
		}
		resp.Checks[name] = "ok"
	}
	c.JSON(code, resp)
}
