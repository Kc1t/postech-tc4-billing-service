package httputil

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// UUIDParams responde 400 INVALID_REQUEST quando um dos parâmetros de rota informados existe e não
// é um UUID, antes do caso de uso. Rotas sem o parâmetro passam direto. O os_id fica de fora: é o
// identificador do os-service, e o billing só o usa como chave de busca.
func UUIDParams(names ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, name := range names {
			v := c.Param(name)
			if v == "" {
				continue
			}
			if uuid.Validate(v) != nil {
				Abort(c, http.StatusBadRequest, CodeInvalidRequest, "parâmetro '"+name+"' deve ser um UUID")
				return
			}
		}
		c.Next()
	}
}
