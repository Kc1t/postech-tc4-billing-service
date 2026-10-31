package ports

import (
	"context"
	"errors"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
)

//go:generate mockgen -source=./gateway.go -destination=./mocks/gateway.go -package=mocks

// Erros da porta PaymentGateway. A distinção entre transitório e definitivo é o que decide se a
// mensagem volta para a fila (nova tentativa) ou se vira evento de falha para a saga.
var (
	// ErrGatewayUnavailable é falha transitória: rede, timeout, 5xx, 429, circuito aberto ou
	// credencial rejeitada (problema de configuração, corrigível sem perder a cobrança).
	ErrGatewayUnavailable = errors.New("gateway de pagamento indisponível")
	// ErrGatewayRejected é recusa definitiva do provedor (4xx de validação).
	ErrGatewayRejected = errors.New("gateway de pagamento recusou a operação")
	// ErrGatewayNotFound indica recurso inexistente no provedor.
	ErrGatewayNotFound = errors.New("recurso não encontrado no gateway de pagamento")
)

// ProviderStatus é o status do pagamento no provedor (valores de GET /v1/payments/{id} do MP).
type ProviderStatus string

const (
	ProviderStatusApproved    ProviderStatus = "approved"
	ProviderStatusPending     ProviderStatus = "pending"
	ProviderStatusInProcess   ProviderStatus = "in_process"
	ProviderStatusAuthorized  ProviderStatus = "authorized"
	ProviderStatusInMediation ProviderStatus = "in_mediation"
	ProviderStatusRejected    ProviderStatus = "rejected"
	ProviderStatusCancelled   ProviderStatus = "cancelled"
	ProviderStatusRefunded    ProviderStatus = "refunded"
	ProviderStatusChargedBack ProviderStatus = "charged_back"
)

// ProviderPayment é o pagamento como o provedor o vê.
type ProviderPayment struct {
	ID                string
	Status            ProviderStatus
	StatusDetail      string
	Amount            float64
	ExternalReference string // ID do nosso Payment (external_reference da preferência)
}

// CheckoutRequest são os dados da cobrança a criar no provedor.
type CheckoutRequest struct {
	PaymentID  string // vai como external_reference: é como o webhook volta ao nosso pagamento
	BudgetID   string
	OSCode     int
	Amount     float64
	PayerEmail string
	PayerName  string
}

// PaymentGateway é a porta para o provedor de pagamento (Mercado Pago ou fake).
type PaymentGateway interface {
	// Provider identifica o adapter (gravado no pagamento).
	Provider() entities.Provider
	// CreateCheckout cria a cobrança e devolve o ID da preferência e o link de pagamento.
	CreateCheckout(ctx context.Context, req CheckoutRequest) (preferenceID, checkoutURL string, err error)
	// GetPayment consulta o pagamento no provedor (fonte da verdade depois de um webhook).
	GetPayment(ctx context.Context, providerPaymentID string) (ProviderPayment, error)
	// Refund estorna integralmente um pagamento aprovado. A mesma idempotencyKey em novas
	// tentativas garante um único estorno.
	Refund(ctx context.Context, providerPaymentID, idempotencyKey string) error
	// Cancel cancela no provedor um pagamento ainda em análise.
	Cancel(ctx context.Context, providerPaymentID string) error
}

// WebhookVerifier valida a assinatura das notificações do provedor.
type WebhookVerifier interface {
	// Verify confere o header de assinatura contra o ID do recurso (da query string) e o ID da
	// requisição. Deve comparar em tempo constante.
	Verify(signature, requestID, dataID string) bool
}
