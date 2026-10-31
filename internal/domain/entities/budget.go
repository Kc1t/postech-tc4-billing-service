package entities

import (
	"fmt"
	"strings"
	"time"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
	"github.com/google/uuid"
)

// BudgetStatus é o status do orçamento.
type BudgetStatus string

const (
	BudgetPendingApproval BudgetStatus = "pending_approval"
	BudgetApproved        BudgetStatus = "approved"
	BudgetRejected        BudgetStatus = "rejected"
	BudgetCancelled       BudgetStatus = "cancelled"
)

// budgetTransitions é a máquina de estados do orçamento:
//
//	pending_approval ──approve──▶ approved ──cancel──▶ cancelled
//	        │ ╲──reject──▶ rejected
//	        ╰────cancel───▶ cancelled
//
// approved → cancelled existe para a compensação da saga (pagamento recusado, reparo rejeitado
// ou prazo esgotado). rejected e cancelled são finais.
var budgetTransitions = map[BudgetStatus][]BudgetStatus{
	BudgetPendingApproval: {BudgetApproved, BudgetRejected, BudgetCancelled},
	BudgetApproved:        {BudgetCancelled},
}

// ItemKind diz se o item é mão de obra (serviço) ou peça.
type ItemKind string

const (
	ItemService ItemKind = "service"
	ItemPart    ItemKind = "part"
)

// BudgetItem é um value object: uma linha do orçamento.
type BudgetItem struct {
	Kind        ItemKind
	Ref         string // código do serviço ou código do fabricante da peça
	Description string
	Quantity    int
	UnitPrice   float64
}

// Subtotal é quantidade × preço unitário, arredondado ao centavo.
func (i BudgetItem) Subtotal() float64 { return fromCents(int64(i.Quantity) * toCents(i.UnitPrice)) }

// Requester é o cliente dono da OS, copiado do comando CreateBudget (o billing não consulta o
// cadastro do os-service; guarda o que precisa para a checagem de posse e para o checkout).
type Requester struct {
	ID       string
	Name     string
	Email    string
	Document string
}

// NewBudgetInput são os dados para abrir um orçamento.
type NewBudgetInput struct {
	OSID      string
	OSCode    int
	SagaID    string
	Requester Requester
	Items     []BudgetItem
	Total     float64 // total informado pelo orquestrador; precisa bater com a soma dos itens
}

// Budget é o agregado orçamento.
type Budget struct {
	id        string
	osID      string
	osCode    int
	sagaID    string
	requester Requester
	items     []BudgetItem
	total     float64
	status    BudgetStatus
	decidedAt *time.Time
	reason    string
	version   int
	createdAt time.Time
	updatedAt time.Time
}

// NewBudget valida os dados e cria um orçamento aguardando aprovação do cliente.
func NewBudget(in NewBudgetInput, now time.Time) (*Budget, error) {
	if err := validateBudgetInput(in); err != nil {
		return nil, err
	}
	var totalCents int64
	items := make([]BudgetItem, len(in.Items))
	for i, it := range in.Items {
		it.UnitPrice = RoundMoney(it.UnitPrice)
		it.Description = strings.TrimSpace(it.Description)
		items[i] = it
		totalCents += int64(it.Quantity) * toCents(it.UnitPrice)
	}
	if toCents(in.Total) != totalCents {
		return nil, fmt.Errorf("%w: total informado %.2f difere da soma dos itens %.2f",
			domainerrors.ErrInvalidBudget, in.Total, fromCents(totalCents))
	}
	now = now.UTC()
	req := in.Requester
	req.Document = NormalizeDocument(req.Document)
	return &Budget{
		id:        uuid.NewString(),
		osID:      in.OSID,
		osCode:    in.OSCode,
		sagaID:    in.SagaID,
		requester: req,
		items:     items,
		total:     fromCents(totalCents),
		status:    BudgetPendingApproval,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func validateBudgetInput(in NewBudgetInput) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", domainerrors.ErrInvalidBudget, fmt.Sprintf(format, args...))
	}
	switch {
	case strings.TrimSpace(in.OSID) == "":
		return invalid("os_id obrigatório")
	case strings.TrimSpace(in.SagaID) == "":
		return invalid("saga_id obrigatório")
	case in.OSCode <= 0:
		return invalid("os_code deve ser positivo")
	case len(NormalizeDocument(in.Requester.Document)) != 11 && len(NormalizeDocument(in.Requester.Document)) != 14:
		return invalid("documento do cliente deve ser CPF (11 dígitos) ou CNPJ (14 dígitos)")
	case len(in.Items) == 0:
		return invalid("orçamento sem itens")
	case in.Total < 0:
		return invalid("total negativo")
	}
	for i, it := range in.Items {
		switch {
		case it.Kind != ItemService && it.Kind != ItemPart:
			return invalid("item %d: tipo %q desconhecido", i, it.Kind)
		case strings.TrimSpace(it.Ref) == "":
			return invalid("item %d: referência obrigatória", i)
		case it.Quantity <= 0:
			return invalid("item %d: quantidade deve ser positiva", i)
		case it.UnitPrice < 0:
			return invalid("item %d: preço unitário negativo", i)
		}
	}
	return nil
}

// Approve registra a aprovação do cliente.
func (b *Budget) Approve(now time.Time) error {
	if err := b.transition(BudgetApproved, now); err != nil {
		return err
	}
	at := now.UTC()
	b.decidedAt = &at
	return nil
}

// Reject registra a recusa do cliente; o motivo é obrigatório.
func (b *Budget) Reject(reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: motivo da recusa obrigatório", domainerrors.ErrInvalidInput)
	}
	if err := b.transition(BudgetRejected, now); err != nil {
		return err
	}
	at := now.UTC()
	b.decidedAt = &at
	b.reason = reason
	return nil
}

// Cancel cancela o orçamento (compensação da saga).
func (b *Budget) Cancel(reason string, now time.Time) error {
	if err := b.transition(BudgetCancelled, now); err != nil {
		return err
	}
	b.reason = strings.TrimSpace(reason)
	return nil
}

// RequireApproved devolve ErrBudgetNotApproved se o orçamento não pode ser cobrado.
func (b *Budget) RequireApproved() error {
	if b.status != BudgetApproved {
		return fmt.Errorf("%w: status atual %s", domainerrors.ErrBudgetNotApproved, b.status)
	}
	return nil
}

func (b *Budget) transition(to BudgetStatus, now time.Time) error {
	for _, allowed := range budgetTransitions[b.status] {
		if allowed == to {
			b.status = to
			b.updatedAt = now.UTC()
			return nil
		}
	}
	return fmt.Errorf("%w: orçamento %s → %s", domainerrors.ErrInvalidTransition, b.status, to)
}

func (b *Budget) ID() string              { return b.id }
func (b *Budget) OSID() string            { return b.osID }
func (b *Budget) OSCode() int             { return b.osCode }
func (b *Budget) SagaID() string          { return b.sagaID }
func (b *Budget) Requester() Requester    { return b.requester }
func (b *Budget) Total() float64          { return b.total }
func (b *Budget) Status() BudgetStatus    { return b.status }
func (b *Budget) DecidedAt() *time.Time   { return b.decidedAt }
func (b *Budget) Reason() string          { return b.reason }
func (b *Budget) Version() int            { return b.version }
func (b *Budget) CreatedAt() time.Time    { return b.createdAt }
func (b *Budget) UpdatedAt() time.Time    { return b.updatedAt }
func (b *Budget) Items() []BudgetItem     { return append([]BudgetItem(nil), b.items...) }
func (b *Budget) OwnedBy(doc string) bool { return NormalizeDocument(doc) == b.requester.Document }

// BudgetSnapshot é a forma plana do agregado, usada pela persistência para salvar e reconstituir.
type BudgetSnapshot struct {
	ID        string
	OSID      string
	OSCode    int
	SagaID    string
	Requester Requester
	Items     []BudgetItem
	Total     float64
	Status    BudgetStatus
	DecidedAt *time.Time
	Reason    string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Snapshot exporta o estado do agregado.
func (b *Budget) Snapshot() BudgetSnapshot {
	return BudgetSnapshot{
		ID: b.id, OSID: b.osID, OSCode: b.osCode, SagaID: b.sagaID, Requester: b.requester,
		Items: b.Items(), Total: b.total, Status: b.status, DecidedAt: b.decidedAt, Reason: b.reason,
		Version: b.version, CreatedAt: b.createdAt, UpdatedAt: b.updatedAt,
	}
}

// RestoreBudget reconstitui um orçamento persistido (sem validar: o estado já foi validado ao ser criado).
func RestoreBudget(s BudgetSnapshot) *Budget {
	return &Budget{
		id: s.ID, osID: s.OSID, osCode: s.OSCode, sagaID: s.SagaID, requester: s.Requester,
		items: append([]BudgetItem(nil), s.Items...), total: s.Total, status: s.Status,
		decidedAt: s.DecidedAt, reason: s.Reason, version: s.Version,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt,
	}
}
