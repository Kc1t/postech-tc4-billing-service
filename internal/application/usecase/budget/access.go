package budgetuc

import (
	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/contracts"
)

// metaFor monta a identificação dos eventos disparados pela API (sem envelope de origem).
func metaFor(b *entities.Budget, correlationID string) contracts.Meta {
	return contracts.Meta{SagaID: b.SagaID(), OSID: b.OSID(), CorrelationID: correlationID}
}
