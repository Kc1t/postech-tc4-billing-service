package middleware

import (
	"net/http"
	"strings"

	httputil "github.com/Kc1t/postech-tc4-billing-service/internal/adapters/inbound/http"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Claims são as claims emitidas pela Lambda de autenticação (clientes) e pelo os-service (equipe).
type Claims struct {
	Role     string `json:"role"`
	Document string `json:"document,omitempty"`
	Email    string `json:"email,omitempty"`
	Name     string `json:"name,omitempty"`
	jwt.RegisteredClaims
}

// Auth valida o JWT HS256 do header Authorization. Só HS256 é aceito (WithValidMethods barra
// "none" e a troca de algoritmo), exp é obrigatório e sub/role precisam estar preenchidos com um
// papel conhecido. O documento (CPF) fica no contexto para a checagem de dono, nunca em log.
func Auth(secret string) gin.HandlerFunc {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	key := []byte(secret)
	return func(c *gin.Context) {
		scheme, raw, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "bearer") || strings.TrimSpace(raw) == "" {
			unauthorized(c, "authorization header with bearer token required")
			return
		}
		var claims Claims
		if _, err := parser.ParseWithClaims(strings.TrimSpace(raw), &claims, func(*jwt.Token) (any, error) { return key, nil }); err != nil {
			unauthorized(c, "invalid or expired token")
			return
		}
		role := entities.Role(claims.Role)
		if claims.Subject == "" || !role.IsValid() {
			unauthorized(c, "incomplete token claims")
			return
		}
		c.Set(httputil.KeyUserID, claims.Subject)
		c.Set(httputil.KeyUserRole, string(role))
		if claims.Document != "" {
			c.Set(httputil.KeyUserDocument, claims.Document)
		}
		c.Next()
	}
}

// RequireRole libera a rota só para os papéis informados (depois de Auth).
func RequireRole(allowed ...entities.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := entities.Role(c.GetString(httputil.KeyUserRole))
		for _, a := range allowed {
			if role == a {
				c.Next()
				return
			}
		}
		httputil.Abort(c, http.StatusForbidden, httputil.CodeForbidden, "insufficient permissions")
	}
}

func unauthorized(c *gin.Context, msg string) {
	c.Header("WWW-Authenticate", `Bearer realm="billing-service"`)
	httputil.Abort(c, http.StatusUnauthorized, httputil.CodeUnauthorized, msg)
}
