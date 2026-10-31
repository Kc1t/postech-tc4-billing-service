package httputil

import (
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/gin-gonic/gin"
)

// Chaves do gin.Context preenchidas pelos middlewares (cmd/api/middleware).
const (
	KeyCorrelationID = "correlation_id"
	KeyUserID        = "user_id"
	KeyUserRole      = "user_role"
	KeyUserDocument  = "user_document"
)

// ActorFrom monta o usuário autenticado a partir das claims do JWT.
func ActorFrom(c *gin.Context) entities.Actor {
	return entities.Actor{
		UserID:   c.GetString(KeyUserID),
		Role:     entities.Role(c.GetString(KeyUserRole)),
		Document: c.GetString(KeyUserDocument),
	}
}

// CorrelationID devolve o X-Correlation-ID da requisição.
func CorrelationID(c *gin.Context) string { return c.GetString(KeyCorrelationID) }
