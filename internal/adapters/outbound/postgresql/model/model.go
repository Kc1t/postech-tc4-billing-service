// Package model contém os modelos GORM (forma das tabelas) e a tradução de/para o domínio.
// O esquema em si vem das migrations SQL; estes structs só mapeiam colunas.
package model

import (
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
)

// Item é a forma de cada linha de orçamento na coluna jsonb items (mesmos campos de contracts.Item).
type Item struct {
	Kind        string  `json:"kind"`
	Ref         string  `json:"ref"`
	Description string  `json:"description"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
}

// Budget é a linha da tabela budgets.
type Budget struct {
	ID                string     `gorm:"column:id;type:uuid;primaryKey"`
	OSID              string     `gorm:"column:os_id"`
	OSCode            int        `gorm:"column:os_code"`
	SagaID            string     `gorm:"column:saga_id"`
	RequesterID       string     `gorm:"column:requester_id"`
	RequesterDocument string     `gorm:"column:requester_document"`
	RequesterName     string     `gorm:"column:requester_name"`
	RequesterEmail    string     `gorm:"column:requester_email"`
	Items             []Item     `gorm:"column:items;type:jsonb;serializer:json"`
	Total             float64    `gorm:"column:total;type:numeric(12,2)"`
	Status            string     `gorm:"column:status"`
	DecidedAt         *time.Time `gorm:"column:decided_at"`
	Reason            string     `gorm:"column:reason"`
	Version           int        `gorm:"column:version"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (Budget) TableName() string { return "budgets" }

// FromBudget converte o agregado na linha da tabela.
func FromBudget(b *entities.Budget) Budget {
	s := b.Snapshot()
	items := make([]Item, len(s.Items))
	for i, it := range s.Items {
		items[i] = Item{Kind: string(it.Kind), Ref: it.Ref, Description: it.Description, Quantity: it.Quantity, UnitPrice: it.UnitPrice}
	}
	return Budget{
		ID: s.ID, OSID: s.OSID, OSCode: s.OSCode, SagaID: s.SagaID,
		RequesterID: s.Requester.ID, RequesterDocument: s.Requester.Document,
		RequesterName: s.Requester.Name, RequesterEmail: s.Requester.Email,
		Items: items, Total: s.Total, Status: string(s.Status), DecidedAt: s.DecidedAt,
		Reason: s.Reason, Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// ToDomain reconstitui o agregado.
func (m Budget) ToDomain() *entities.Budget {
	items := make([]entities.BudgetItem, len(m.Items))
	for i, it := range m.Items {
		items[i] = entities.BudgetItem{Kind: entities.ItemKind(it.Kind), Ref: it.Ref, Description: it.Description, Quantity: it.Quantity, UnitPrice: it.UnitPrice}
	}
	return entities.RestoreBudget(entities.BudgetSnapshot{
		ID: m.ID, OSID: m.OSID, OSCode: m.OSCode, SagaID: m.SagaID,
		Requester: entities.Requester{ID: m.RequesterID, Name: m.RequesterName, Email: m.RequesterEmail, Document: m.RequesterDocument},
		Items:     items, Total: m.Total, Status: entities.BudgetStatus(m.Status), DecidedAt: utcPtr(m.DecidedAt),
		Reason: m.Reason, Version: m.Version, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
	})
}

// Payment é a linha da tabela payments. Os IDs do provedor são ponteiros para virarem NULL
// quando vazios (o índice único de provider_payment_id só vale para valores presentes).
type Payment struct {
	ID                   string     `gorm:"column:id;type:uuid;primaryKey"`
	BudgetID             string     `gorm:"column:budget_id;type:uuid"`
	OSID                 string     `gorm:"column:os_id"`
	Amount               float64    `gorm:"column:amount;type:numeric(12,2)"`
	Status               string     `gorm:"column:status"`
	Provider             string     `gorm:"column:provider"`
	ProviderPreferenceID *string    `gorm:"column:provider_preference_id"`
	ProviderPaymentID    *string    `gorm:"column:provider_payment_id"`
	CheckoutURL          *string    `gorm:"column:checkout_url"`
	FailureReason        string     `gorm:"column:failure_reason"`
	RefundedAt           *time.Time `gorm:"column:refunded_at"`
	Version              int        `gorm:"column:version"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (Payment) TableName() string { return "payments" }

// FromPayment converte o agregado na linha da tabela.
func FromPayment(p *entities.Payment) Payment {
	s := p.Snapshot()
	return Payment{
		ID: s.ID, BudgetID: s.BudgetID, OSID: s.OSID, Amount: s.Amount, Status: string(s.Status),
		Provider: string(s.Provider), ProviderPreferenceID: nullable(s.ProviderPreferenceID),
		ProviderPaymentID: nullable(s.ProviderPaymentID), CheckoutURL: nullable(s.CheckoutURL),
		FailureReason: s.FailureReason, RefundedAt: s.RefundedAt, Version: s.Version,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
}

// ToDomain reconstitui o agregado.
func (m Payment) ToDomain() *entities.Payment {
	return entities.RestorePayment(entities.PaymentSnapshot{
		ID: m.ID, BudgetID: m.BudgetID, OSID: m.OSID, Amount: m.Amount, Status: entities.PaymentStatus(m.Status),
		Provider: entities.Provider(m.Provider), ProviderPreferenceID: deref(m.ProviderPreferenceID),
		ProviderPaymentID: deref(m.ProviderPaymentID), CheckoutURL: deref(m.CheckoutURL),
		FailureReason: m.FailureReason, RefundedAt: utcPtr(m.RefundedAt), Version: m.Version,
		CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
	})
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
