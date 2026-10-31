// Package presenter define o formato JSON das respostas da API e a conversão a partir do domínio.
package presenter

import (
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// ItemResponse é uma linha do orçamento.
type ItemResponse struct {
	Kind        string  `json:"kind" example:"part"`
	Ref         string  `json:"ref" example:"PST-DIANT"`
	Description string  `json:"description" example:"Jogo de pastilhas dianteiras"`
	Quantity    int     `json:"quantity" example:"1"`
	UnitPrice   float64 `json:"unit_price" example:"219.9"`
	Subtotal    float64 `json:"subtotal" example:"219.9"`
}

// RequesterResponse identifica o cliente (sem o documento: minimização de dados pessoais).
type RequesterResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name" example:"Maria Silva"`
	Email string `json:"email" example:"maria@example.com"`
}

// PaymentResponse é a cobrança de um orçamento.
type PaymentResponse struct {
	ID                string     `json:"id" example:"7a2d3b8f-0002-4b62-8b4f-000000000002"`
	BudgetID          string     `json:"budget_id"`
	OSID              string     `json:"os_id"`
	Status            string     `json:"status" example:"pending" enums:"created,pending,approved,failed,cancelled,refunded"`
	Amount            float64    `json:"amount" example:"399.9"`
	Provider          string     `json:"provider" example:"mercadopago" enums:"mercadopago,fake"`
	CheckoutURL       string     `json:"checkout_url,omitempty" example:"https://www.mercadopago.com.br/checkout/v1/redirect?pref_id=..."`
	ProviderPaymentID string     `json:"provider_payment_id,omitempty" example:"1325432423"`
	FailureReason     string     `json:"failure_reason,omitempty"`
	RefundedAt        *time.Time `json:"refunded_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// BudgetResponse é o orçamento com a cobrança mais recente.
type BudgetResponse struct {
	ID        string            `json:"id" example:"6f1c2a7e-0002-4a51-9a3e-000000000002"`
	OSID      string            `json:"os_id"`
	OSCode    int               `json:"os_code" example:"9002"`
	SagaID    string            `json:"saga_id"`
	Status    string            `json:"status" example:"pending_approval" enums:"pending_approval,approved,rejected,cancelled"`
	Requester RequesterResponse `json:"requester"`
	Items     []ItemResponse    `json:"items"`
	Total     float64           `json:"total" example:"399.9"`
	DecidedAt *time.Time        `json:"decided_at,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Payment   *PaymentResponse  `json:"payment"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// Budget converte o orçamento (com ou sem cobrança).
func Budget(b *entities.Budget, p *entities.Payment) BudgetResponse {
	items := make([]ItemResponse, 0, len(b.Items()))
	for _, it := range b.Items() {
		items = append(items, ItemResponse{
			Kind: string(it.Kind), Ref: it.Ref, Description: it.Description,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, Subtotal: it.Subtotal(),
		})
	}
	r := b.Requester()
	resp := BudgetResponse{
		ID: b.ID(), OSID: b.OSID(), OSCode: b.OSCode(), SagaID: b.SagaID(), Status: string(b.Status()),
		Requester: RequesterResponse{ID: r.ID, Name: r.Name, Email: r.Email},
		Items:     items, Total: b.Total(), DecidedAt: b.DecidedAt(), Reason: b.Reason(),
		CreatedAt: b.CreatedAt(), UpdatedAt: b.UpdatedAt(),
	}
	if p != nil {
		pr := Payment(p)
		resp.Payment = &pr
	}
	return resp
}

// View converte a consulta do caso de uso.
func View(v ports.BudgetView) BudgetResponse { return Budget(v.Budget, v.Payment) }

// Payment converte a cobrança.
func Payment(p *entities.Payment) PaymentResponse {
	return PaymentResponse{
		ID: p.ID(), BudgetID: p.BudgetID(), OSID: p.OSID(), Status: string(p.Status()), Amount: p.Amount(),
		Provider: string(p.Provider()), CheckoutURL: p.CheckoutURL(), ProviderPaymentID: p.ProviderPaymentID(),
		FailureReason: p.FailureReason(), RefundedAt: p.RefundedAt(), CreatedAt: p.CreatedAt(), UpdatedAt: p.UpdatedAt(),
	}
}
