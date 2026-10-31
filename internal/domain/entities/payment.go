package entities

import (
	"fmt"
	"strings"
	"time"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/google/uuid"
)

// PaymentStatus é o status do pagamento.
type PaymentStatus string

const (
	PaymentCreated   PaymentStatus = "created"
	PaymentPending   PaymentStatus = "pending"
	PaymentApproved  PaymentStatus = "approved"
	PaymentFailed    PaymentStatus = "failed"
	PaymentCancelled PaymentStatus = "cancelled"
	PaymentRefunded  PaymentStatus = "refunded"
)

// paymentTransitions é a máquina de estados do pagamento:
//
//	created ──link──▶ pending ──approve──▶ approved ──refund──▶ refunded
//	   │                 │
//	   ├──fail/cancel──▶ failed | cancelled ◀──fail/cancel──╯
//
// failed, cancelled e refunded são finais.
var paymentTransitions = map[PaymentStatus][]PaymentStatus{
	PaymentCreated:  {PaymentPending, PaymentFailed, PaymentCancelled},
	PaymentPending:  {PaymentApproved, PaymentFailed, PaymentCancelled},
	PaymentApproved: {PaymentRefunded},
}

// Provider identifica o provedor de pagamento.
type Provider string

const (
	ProviderMercadoPago Provider = "mercadopago"
	ProviderFake        Provider = "fake"
)

// Payment é o agregado pagamento: uma tentativa de cobrança de um orçamento aprovado.
type Payment struct {
	id                   string
	budgetID             string
	osID                 string
	amount               float64
	status               PaymentStatus
	provider             Provider
	providerPreferenceID string
	providerPaymentID    string
	checkoutURL          string
	failureReason        string
	refundedAt           *time.Time
	version              int
	createdAt            time.Time
	updatedAt            time.Time
}

// NewPayment cria a cobrança de um orçamento. Só orçamentos aprovados podem ser cobrados.
func NewPayment(b *Budget, provider Provider, now time.Time) (*Payment, error) {
	if err := b.RequireApproved(); err != nil {
		return nil, err
	}
	now = now.UTC()
	return &Payment{
		id:        uuid.NewString(),
		budgetID:  b.ID(),
		osID:      b.OSID(),
		amount:    b.Total(),
		status:    PaymentCreated,
		provider:  provider,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// LinkCreated registra a preferência criada no provedor: o cliente já pode pagar.
func (p *Payment) LinkCreated(preferenceID, checkoutURL string, now time.Time) error {
	if strings.TrimSpace(checkoutURL) == "" {
		return fmt.Errorf("%w: checkout_url vazio", domainerrors.ErrInvalidInput)
	}
	if err := p.transition(PaymentPending, now); err != nil {
		return err
	}
	p.providerPreferenceID = preferenceID
	p.checkoutURL = checkoutURL
	return nil
}

// AttachProviderPayment guarda o ID do pagamento no provedor enquanto ele ainda está em análise
// (pending/in_process): é o que permite cancelá-lo no provedor se o orçamento for cancelado.
func (p *Payment) AttachProviderPayment(providerPaymentID string, now time.Time) bool {
	if p.status != PaymentPending || p.providerPaymentID != "" || providerPaymentID == "" {
		return false
	}
	p.providerPaymentID = providerPaymentID
	p.updatedAt = now.UTC()
	return true
}

// Approve confirma o pagamento. O valor pago precisa ser o valor cobrado.
func (p *Payment) Approve(providerPaymentID string, paidAmount float64, now time.Time) error {
	if strings.TrimSpace(providerPaymentID) == "" {
		return fmt.Errorf("%w: id do pagamento no provedor obrigatório", domainerrors.ErrInvalidInput)
	}
	if !SameAmount(paidAmount, p.amount) {
		return fmt.Errorf("%w: pago %.2f, cobrado %.2f", domainerrors.ErrAmountMismatch, paidAmount, p.amount)
	}
	if err := p.transition(PaymentApproved, now); err != nil {
		return err
	}
	p.providerPaymentID = providerPaymentID
	return nil
}

// Fail encerra a tentativa como falha definitiva (recusa do provedor, erro definitivo na criação
// do checkout ou falha simulada).
func (p *Payment) Fail(reason, providerPaymentID string, now time.Time) error {
	if err := p.transition(PaymentFailed, now); err != nil {
		return err
	}
	p.failureReason = strings.TrimSpace(reason)
	if p.providerPaymentID == "" {
		p.providerPaymentID = providerPaymentID
	}
	return nil
}

// Cancel cancela uma cobrança ainda em aberto (compensação: orçamento cancelado).
func (p *Payment) Cancel(reason string, now time.Time) error {
	if err := p.transition(PaymentCancelled, now); err != nil {
		return err
	}
	p.failureReason = strings.TrimSpace(reason)
	return nil
}

// Refund registra o estorno de um pagamento aprovado.
func (p *Payment) Refund(now time.Time) error {
	if err := p.transition(PaymentRefunded, now); err != nil {
		return err
	}
	at := now.UTC()
	p.refundedAt = &at
	return nil
}

// IsOpen diz se a cobrança ainda pode ser paga (created ou pending).
func (p *Payment) IsOpen() bool { return p.status == PaymentCreated || p.status == PaymentPending }

func (p *Payment) transition(to PaymentStatus, now time.Time) error {
	for _, allowed := range paymentTransitions[p.status] {
		if allowed == to {
			p.status = to
			p.updatedAt = now.UTC()
			return nil
		}
	}
	return fmt.Errorf("%w: pagamento %s → %s", domainerrors.ErrInvalidTransition, p.status, to)
}

func (p *Payment) ID() string                   { return p.id }
func (p *Payment) BudgetID() string             { return p.budgetID }
func (p *Payment) OSID() string                 { return p.osID }
func (p *Payment) Amount() float64              { return p.amount }
func (p *Payment) Status() PaymentStatus        { return p.status }
func (p *Payment) Provider() Provider           { return p.provider }
func (p *Payment) ProviderPreferenceID() string { return p.providerPreferenceID }
func (p *Payment) ProviderPaymentID() string    { return p.providerPaymentID }
func (p *Payment) CheckoutURL() string          { return p.checkoutURL }
func (p *Payment) FailureReason() string        { return p.failureReason }
func (p *Payment) RefundedAt() *time.Time       { return p.refundedAt }
func (p *Payment) Version() int                 { return p.version }
func (p *Payment) CreatedAt() time.Time         { return p.createdAt }
func (p *Payment) UpdatedAt() time.Time         { return p.updatedAt }

// PaymentSnapshot é a forma plana do agregado para a persistência.
type PaymentSnapshot struct {
	ID                   string
	BudgetID             string
	OSID                 string
	Amount               float64
	Status               PaymentStatus
	Provider             Provider
	ProviderPreferenceID string
	ProviderPaymentID    string
	CheckoutURL          string
	FailureReason        string
	RefundedAt           *time.Time
	Version              int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Snapshot exporta o estado do agregado.
func (p *Payment) Snapshot() PaymentSnapshot {
	return PaymentSnapshot{
		ID: p.id, BudgetID: p.budgetID, OSID: p.osID, Amount: p.amount, Status: p.status,
		Provider: p.provider, ProviderPreferenceID: p.providerPreferenceID,
		ProviderPaymentID: p.providerPaymentID, CheckoutURL: p.checkoutURL,
		FailureReason: p.failureReason, RefundedAt: p.refundedAt, Version: p.version,
		CreatedAt: p.createdAt, UpdatedAt: p.updatedAt,
	}
}

// RestorePayment reconstitui um pagamento persistido.
func RestorePayment(s PaymentSnapshot) *Payment {
	return &Payment{
		id: s.ID, budgetID: s.BudgetID, osID: s.OSID, amount: s.Amount, status: s.Status,
		provider: s.Provider, providerPreferenceID: s.ProviderPreferenceID,
		providerPaymentID: s.ProviderPaymentID, checkoutURL: s.CheckoutURL,
		failureReason: s.FailureReason, refundedAt: s.RefundedAt, version: s.Version,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt,
	}
}
