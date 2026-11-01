// Package httputil reúne o que os handlers HTTP compartilham: o formato padronizado de erro, a
// tradução dos erros do domínio para status/código e a leitura do usuário autenticado que os
// middlewares deixam no contexto do Gin.
package httputil

import (
	"errors"
	"net/http"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
	"github.com/gin-gonic/gin"
)

// Códigos de erro da API (tabela no README).
const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeInvalidSignature   = "INVALID_SIGNATURE"
	CodeForbidden          = "FORBIDDEN"
	CodeOriginNotAllowed   = "ORIGIN_NOT_ALLOWED"
	CodeSimulationDisabled = "SIMULATION_DISABLED"
	CodeNotFound           = "NOT_FOUND"
	CodeBudgetNotFound     = "BUDGET_NOT_FOUND"
	CodePaymentNotFound    = "PAYMENT_NOT_FOUND"
	CodeInvalidTransition  = "INVALID_TRANSITION"
	CodeBudgetNotApproved  = "BUDGET_NOT_APPROVED"
	CodeConcurrentUpdate   = "CONCURRENT_UPDATE"
	CodeGatewayUnavailable = "GATEWAY_UNAVAILABLE"
	CodeInternal           = "INTERNAL_ERROR"
)

// ErrorBody é o conteúdo do erro.
type ErrorBody struct {
	Code    string `json:"code" example:"BUDGET_NOT_FOUND"`
	Message string `json:"message" example:"orçamento não encontrado"`
}

// ErrorResponse é o formato de toda resposta de erro: {"error":{"code":"...","message":"..."}}.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// Abort encerra a requisição com o erro padronizado.
func Abort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

var errorTable = []struct {
	err    error
	status int
	code   string
}{
	{domainerrors.ErrBudgetNotFound, http.StatusNotFound, CodeBudgetNotFound},
	{domainerrors.ErrPaymentNotFound, http.StatusNotFound, CodePaymentNotFound},
	{domainerrors.ErrNotOwner, http.StatusForbidden, CodeForbidden},
	{domainerrors.ErrSimulationDisabled, http.StatusForbidden, CodeSimulationDisabled},
	{domainerrors.ErrInvalidTransition, http.StatusConflict, CodeInvalidTransition},
	{domainerrors.ErrBudgetNotApproved, http.StatusConflict, CodeBudgetNotApproved},
	{domainerrors.ErrConcurrentUpdate, http.StatusConflict, CodeConcurrentUpdate},
	{domainerrors.ErrInvalidInput, http.StatusBadRequest, CodeInvalidRequest},
	{ports.ErrGatewayUnavailable, http.StatusServiceUnavailable, CodeGatewayUnavailable},
}

// WriteError traduz um erro do caso de uso. Erros não mapeados viram 500 com mensagem genérica
// (o detalhe vai só para o log, nunca para o cliente).
func WriteError(c *gin.Context, err error) {
	for _, m := range errorTable {
		if errors.Is(err, m.err) {
			c.Error(err) //nolint:errcheck // registra no contexto para o RequestLogger
			Abort(c, m.status, m.code, err.Error())
			return
		}
	}
	logger.FromContext(c.Request.Context()).Error("request_failed", "error", err)
	c.Error(err) //nolint:errcheck // idem
	Abort(c, http.StatusInternalServerError, CodeInternal, "erro interno; tente novamente")
}

// BadRequest responde 400 para corpo ou parâmetro inválido, com a mensagem de BindingMessage.
func BadRequest(c *gin.Context, err error) {
	Abort(c, http.StatusBadRequest, CodeInvalidRequest, BindingMessage(err))
}
