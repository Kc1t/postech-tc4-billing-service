package entities

import (
	"errors"
	"testing"
	"time"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
)

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func validInput() NewBudgetInput {
	return NewBudgetInput{
		OSID:   "os-1",
		OSCode: 42,
		SagaID: "saga-1",
		Requester: Requester{
			ID: "req-1", Name: "Maria", Email: "maria@example.com", Document: "529.982.247-25",
		},
		Items: []BudgetItem{
			{Kind: ItemService, Ref: "1", Description: " Troca de óleo ", Quantity: 1, UnitPrice: 120},
			{Kind: ItemPart, Ref: "FLT-01", Description: "Filtro", Quantity: 3, UnitPrice: 10.1},
		},
		Total: 150.3,
	}
}

func newBudget(t *testing.T) *Budget {
	t.Helper()
	b, err := NewBudget(validInput(), t0)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	return b
}

func TestNewBudget_Valido(t *testing.T) {
	b := newBudget(t)
	if b.ID() == "" || b.Status() != BudgetPendingApproval || b.Version() != 1 {
		t.Fatalf("estado inicial errado: id=%q status=%s version=%d", b.ID(), b.Status(), b.Version())
	}
	if b.Total() != 150.3 {
		t.Fatalf("total = %v, esperava 150.30", b.Total())
	}
	if b.Requester().Document != "52998224725" {
		t.Fatalf("documento deveria ser normalizado, veio %q", b.Requester().Document)
	}
	if b.Items()[0].Description != "Troca de óleo" {
		t.Fatalf("descrição deveria ser aparada: %q", b.Items()[0].Description)
	}
	if b.Items()[1].Subtotal() != 30.3 {
		t.Fatalf("subtotal = %v", b.Items()[1].Subtotal())
	}
	if b.OSID() != "os-1" || b.OSCode() != 42 || b.SagaID() != "saga-1" || !b.CreatedAt().Equal(t0) || !b.UpdatedAt().Equal(t0) {
		t.Fatal("campos de identificação não copiados")
	}
	if !b.OwnedBy("52998224725") || b.OwnedBy("11144477735") {
		t.Fatal("OwnedBy errado")
	}
}

func TestNewBudget_Invalido(t *testing.T) {
	cases := map[string]func(*NewBudgetInput){
		"sem os_id":          func(in *NewBudgetInput) { in.OSID = " " },
		"sem saga_id":        func(in *NewBudgetInput) { in.SagaID = "" },
		"os_code zero":       func(in *NewBudgetInput) { in.OSCode = 0 },
		"documento curto":    func(in *NewBudgetInput) { in.Requester.Document = "123" },
		"sem itens":          func(in *NewBudgetInput) { in.Items = nil },
		"total negativo":     func(in *NewBudgetInput) { in.Total = -1 },
		"tipo desconhecido":  func(in *NewBudgetInput) { in.Items[0].Kind = "x" },
		"sem referência":     func(in *NewBudgetInput) { in.Items[0].Ref = "" },
		"quantidade zero":    func(in *NewBudgetInput) { in.Items[0].Quantity = 0 },
		"preço negativo":     func(in *NewBudgetInput) { in.Items[0].UnitPrice = -5 },
		"total divergente":   func(in *NewBudgetInput) { in.Total = 150.31 },
		"total sem os itens": func(in *NewBudgetInput) { in.Total = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			in.Items = append([]BudgetItem(nil), in.Items...)
			mutate(&in)
			if _, err := NewBudget(in, t0); !errors.Is(err, domainerrors.ErrInvalidBudget) {
				t.Fatalf("esperava ErrInvalidBudget, veio %v", err)
			}
		})
	}
}

func TestNewBudget_AceitaCNPJ(t *testing.T) {
	in := validInput()
	in.Requester.Document = "11.222.333/0001-81"
	if _, err := NewBudget(in, t0); err != nil {
		t.Fatalf("CNPJ deveria ser aceito: %v", err)
	}
}

func TestBudget_Approve(t *testing.T) {
	b := newBudget(t)
	later := t0.Add(time.Hour)
	if err := b.Approve(later); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if b.Status() != BudgetApproved || b.DecidedAt() == nil || !b.DecidedAt().Equal(later) || !b.UpdatedAt().Equal(later) {
		t.Fatalf("aprovação não registrada: %+v", b.Snapshot())
	}
	if err := b.RequireApproved(); err != nil {
		t.Fatalf("RequireApproved: %v", err)
	}
	if err := b.Approve(later); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("aprovar duas vezes deveria falhar, veio %v", err)
	}
}

func TestBudget_Reject(t *testing.T) {
	b := newBudget(t)
	if err := b.Reject("  ", t0); !errors.Is(err, domainerrors.ErrInvalidInput) {
		t.Fatalf("recusa sem motivo deveria falhar, veio %v", err)
	}
	if err := b.Reject(" caro demais ", t0); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if b.Status() != BudgetRejected || b.Reason() != "caro demais" || b.DecidedAt() == nil {
		t.Fatalf("recusa não registrada: %+v", b.Snapshot())
	}
	if err := b.RequireApproved(); !errors.Is(err, domainerrors.ErrBudgetNotApproved) {
		t.Fatalf("esperava ErrBudgetNotApproved, veio %v", err)
	}
	if err := b.Cancel("x", t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("recusado é final, veio %v", err)
	}
}

func TestBudget_Cancel(t *testing.T) {
	pending := newBudget(t)
	if err := pending.Cancel(" prazo esgotado ", t0); err != nil || pending.Status() != BudgetCancelled || pending.Reason() != "prazo esgotado" {
		t.Fatalf("cancelar pendente: err=%v status=%s", err, pending.Status())
	}
	approved := newBudget(t)
	_ = approved.Approve(t0)
	if err := approved.Cancel("pagamento recusado", t0); err != nil {
		t.Fatalf("cancelar aprovado (compensação): %v", err)
	}
	if err := approved.Cancel("de novo", t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("cancelado é final, veio %v", err)
	}
}

func TestBudget_SnapshotRestore(t *testing.T) {
	b := newBudget(t)
	_ = b.Approve(t0)
	s := b.Snapshot()
	s.Version = 7
	r := RestoreBudget(s)
	if r.ID() != b.ID() || r.Status() != BudgetApproved || r.Version() != 7 || len(r.Items()) != 2 || r.Total() != b.Total() {
		t.Fatalf("restore perdeu estado: %+v", r.Snapshot())
	}
	// Items devolve cópia: alterar o slice não altera o agregado.
	items := r.Items()
	items[0].Quantity = 99
	if r.Items()[0].Quantity == 99 {
		t.Fatal("Items deveria devolver cópia")
	}
}

func TestDocumentHelpers(t *testing.T) {
	cases := map[string]string{
		"529.982.247-25":     "***.***.***-25",
		"11222333000181":     "**.***.***/****-81",
		"12":                 "***",
		"1234567":            "*****67",
		"11.222.333/0001-81": "**.***.***/****-81",
	}
	for in, want := range cases {
		if got := MaskDocument(in); got != want {
			t.Errorf("MaskDocument(%q) = %q, esperava %q", in, got, want)
		}
	}
	if NormalizeDocument("529.982.247-25") != "52998224725" {
		t.Error("NormalizeDocument")
	}
}

func TestMoney(t *testing.T) {
	if RoundMoney(10.005) != 10.01 || RoundMoney(0.1+0.2) != 0.3 {
		t.Error("RoundMoney")
	}
	if !SameAmount(150.3, 150.30000001) || SameAmount(150.3, 150.31) {
		t.Error("SameAmount")
	}
}

func TestActor(t *testing.T) {
	client := Actor{Role: RoleClient, Document: "529.982.247-25"}
	if !client.Owns("52998224725") || client.Owns("11144477735") || client.IsAdmin() {
		t.Fatal("cliente: posse errada")
	}
	if (Actor{Role: RoleAdmin}).Owns("") || !(Actor{Role: RoleAdmin}).IsAdmin() {
		t.Fatal("admin não é dono de documento")
	}
	if (Actor{Role: RoleClient}).Owns("") {
		t.Fatal("cliente sem documento não é dono de nada")
	}
	for _, r := range []Role{RoleAdmin, RoleMechanic, RoleClient} {
		if !r.IsValid() {
			t.Errorf("%s deveria ser válido", r)
		}
	}
	if Role("root").IsValid() {
		t.Error("papel desconhecido aceito")
	}
}

func TestBudget_Access(t *testing.T) {
	b := newBudget(t)
	owner := Actor{Role: RoleClient, Document: "52998224725"}
	other := Actor{Role: RoleClient, Document: "11144477735"}
	admin := Actor{Role: RoleAdmin}
	mechanic := Actor{Role: RoleMechanic}
	if b.CheckReadAccess(owner) != nil || b.CheckReadAccess(admin) != nil {
		t.Fatal("dono e admin leem")
	}
	if !errors.Is(b.CheckReadAccess(other), domainerrors.ErrNotOwner) || !errors.Is(b.CheckReadAccess(mechanic), domainerrors.ErrNotOwner) {
		t.Fatal("outro cliente e mecânico não leem")
	}
	if b.CheckDecisionAccess(owner) != nil {
		t.Fatal("dono decide")
	}
	if !errors.Is(b.CheckDecisionAccess(admin), domainerrors.ErrNotOwner) || !errors.Is(b.CheckDecisionAccess(other), domainerrors.ErrNotOwner) {
		t.Fatal("só o dono decide")
	}
}
