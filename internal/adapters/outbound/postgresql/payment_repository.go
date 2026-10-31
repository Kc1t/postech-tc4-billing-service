package postgresql

import (
	"context"
	"errors"

	"github.com/Kc1t/postech-tc4-billing-service/internal/adapters/outbound/postgresql/model"
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type paymentRepository struct{ db *gorm.DB }

// NewPaymentRepository cria o repositório sobre db (conexão do pool ou transação).
func NewPaymentRepository(db *gorm.DB) ports.PaymentRepository { return &paymentRepository{db: db} }

// Create grava a cobrança. Violação de unicidade aqui significa que outra operação criou, em
// paralelo, a cobrança viva do mesmo orçamento: devolve ErrConcurrentUpdate para a mensagem ser
// reprocessada e cair no caminho idempotente.
func (r *paymentRepository) Create(ctx context.Context, p *entities.Payment) error {
	m := model.FromPayment(p)
	if err := r.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueViolation(err) {
			return domainerrors.ErrConcurrentUpdate
		}
		return err
	}
	return nil
}

func (r *paymentRepository) FindByID(ctx context.Context, id string) (*entities.Payment, error) {
	if uuid.Validate(id) != nil {
		return nil, domainerrors.ErrPaymentNotFound
	}
	return r.find(r.db.WithContext(ctx).Where("id = ?", id))
}

func (r *paymentRepository) FindLatestByBudgetID(ctx context.Context, budgetID string) (*entities.Payment, error) {
	if uuid.Validate(budgetID) != nil {
		return nil, domainerrors.ErrPaymentNotFound
	}
	return r.find(r.db.WithContext(ctx).Where("budget_id = ?", budgetID).Order("created_at DESC, id DESC"))
}

func (r *paymentRepository) find(q *gorm.DB) (*entities.Payment, error) {
	var m model.Payment
	err := q.Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerrors.ErrPaymentNotFound
	}
	if err != nil {
		return nil, err
	}
	return m.ToDomain(), nil
}

// Update grava os campos mutáveis com optimistic lock.
func (r *paymentRepository) Update(ctx context.Context, p *entities.Payment) error {
	m := model.FromPayment(p)
	res := r.db.WithContext(ctx).Model(&model.Payment{}).
		Where("id = ? AND version = ?", m.ID, m.Version).
		Updates(map[string]any{
			"status":                 m.Status,
			"provider_preference_id": m.ProviderPreferenceID,
			"provider_payment_id":    m.ProviderPaymentID,
			"checkout_url":           m.CheckoutURL,
			"failure_reason":         m.FailureReason,
			"refunded_at":            m.RefundedAt,
			"version":                m.Version + 1,
			"updated_at":             m.UpdatedAt,
		})
	if res.Error != nil {
		if isUniqueViolation(res.Error) {
			return domainerrors.ErrConcurrentUpdate
		}
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domainerrors.ErrConcurrentUpdate
	}
	s := p.Snapshot()
	s.Version++
	*p = *entities.RestorePayment(s)
	return nil
}
