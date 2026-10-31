package middleware

import (
	"crypto/rand"
	"encoding/hex"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/gin-gonic/gin"
)

const (
	// CorrelationHeader é o header de correlação aceito e devolvido em toda resposta.
	CorrelationHeader = "X-Correlation-ID"
	// requestIDHeader é aceito como alternativa (API Gateway e o próprio Mercado Pago enviam).
	requestIDHeader   = "X-Request-ID"
	maxCorrelationLen = 128
)

// CorrelationID reaproveita o X-Correlation-ID (ou X-Request-ID) recebido ou gera um novo. O valor
// vai para os logs da requisição e para o correlation_id dos eventos publicados.
func CorrelationID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(CorrelationHeader)
		if !validCorrelationID(id) {
			id = c.GetHeader(requestIDHeader)
		}
		if !validCorrelationID(id) {
			id = newCorrelationID()
		}
		c.Set(httputil.KeyCorrelationID, id)
		c.Writer.Header().Set(CorrelationHeader, id)
		c.Next()
	}
}

// validCorrelationID aceita só identificadores curtos e de caracteres seguros: o valor vem do
// cliente e acaba em logs e mensagens.
func validCorrelationID(id string) bool {
	if id == "" || len(id) > maxCorrelationLen {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

func newCorrelationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read não falha (Go ≥ 1.24)
	return hex.EncodeToString(b)
}
