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

type budgetRepository struct{ db *gorm.DB }

// NewBudgetRepository cria o repositório sobre db (conexão do pool ou transação).
func NewBudgetRepository(db *gorm.DB) ports.BudgetRepository { return &budgetRepository{db: db} }

func (r *budgetRepository) Create(ctx context.Context, b *entities.Budget) error {
	m := model.FromBudget(b)
	if err := r.db.WithContext(ctx).Create(&m).Error; err != nil {
		if isUniqueViolation(err) {
			return domainerrors.ErrBudgetAlreadyExists
		}
		return err
	}
	return nil
}

func (r *budgetRepository) FindByID(ctx context.Context, id string) (*entities.Budget, error) {
	if uuid.Validate(id) != nil {
		return nil, domainerrors.ErrBudgetNotFound
	}
	return r.find(ctx, "id = ?", id)
}

func (r *budgetRepository) FindByOSID(ctx context.Context, osID string) (*entities.Budget, error) {
	return r.find(ctx, "os_id = ?", osID)
}

func (r *budgetRepository) find(ctx context.Context, where string, arg any) (*entities.Budget, error) {
	var m model.Budget
	err := r.db.WithContext(ctx).Where(where, arg).Take(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, domainerrors.ErrBudgetNotFound
	}
	if err != nil {
		return nil, err
	}
	return m.ToDomain(), nil
}

// Update grava os campos mutáveis com optimistic lock: o UPDATE só casa se a version lida
// ainda é a do banco. Zero linhas afetadas = outra operação gravou antes.
func (r *budgetRepository) Update(ctx context.Context, b *entities.Budget) error {
	s := b.Snapshot()
	res := r.db.WithContext(ctx).Model(&model.Budget{}).
		Where("id = ? AND version = ?", s.ID, s.Version).
		Updates(map[string]any{
			"status":     string(s.Status),
			"decided_at": s.DecidedAt,
			"reason":     s.Reason,
			"version":    s.Version + 1,
			"updated_at": s.UpdatedAt,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return domainerrors.ErrConcurrentUpdate
	}
	s.Version++
	*b = *entities.RestoreBudget(s)
	return nil
}
