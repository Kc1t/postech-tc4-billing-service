package entities

import (
	"errors"
	"testing"
	"time"

	domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"
)

func approvedBudget(t *testing.T) *Budget {
	t.Helper()
	b := newBudget(t)
	if err := b.Approve(t0); err != nil {
		t.Fatal(err)
	}
	return b
}

func newPayment(t *testing.T) *Payment {
	t.Helper()
	p, err := NewPayment(approvedBudget(t), ProviderFake, t0)
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	return p
}

func pendingPayment(t *testing.T) *Payment {
	t.Helper()
	p := newPayment(t)
	if err := p.LinkCreated("pref-1", "https://pay/1", t0); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewPayment(t *testing.T) {
	b := approvedBudget(t)
	p, err := NewPayment(b, ProviderMercadoPago, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() == "" || p.BudgetID() != b.ID() || p.OSID() != b.OSID() || p.Amount() != b.Total() ||
		p.Status() != PaymentCreated || p.Provider() != ProviderMercadoPago || p.Version() != 1 || !p.IsOpen() {
		t.Fatalf("estado inicial errado: %+v", p.Snapshot())
	}
	if !p.CreatedAt().Equal(t0) || !p.UpdatedAt().Equal(t0) {
		t.Fatal("timestamps")
	}
	if _, err := NewPayment(newBudget(t), ProviderFake, t0); !errors.Is(err, domainerrors.ErrBudgetNotApproved) {
		t.Fatalf("orçamento pendente não pode ser cobrado, veio %v", err)
	}
}

func TestPayment_LinkCreated(t *testing.T) {
	p := newPayment(t)
	if err := p.LinkCreated("pref", "", t0); !errors.Is(err, domainerrors.ErrInvalidInput) {
		t.Fatalf("checkout vazio deveria falhar, veio %v", err)
	}
	if err := p.LinkCreated("pref-1", "https://pay/1", t0); err != nil {
		t.Fatal(err)
	}
	if p.Status() != PaymentPending || p.ProviderPreferenceID() != "pref-1" || p.CheckoutURL() != "https://pay/1" {
		t.Fatalf("link não registrado: %+v", p.Snapshot())
	}
	if err := p.LinkCreated("pref-2", "https://pay/2", t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("link duas vezes deveria falhar, veio %v", err)
	}
}

func TestPayment_Approve(t *testing.T) {
	p := pendingPayment(t)
	if err := p.Approve("", p.Amount(), t0); !errors.Is(err, domainerrors.ErrInvalidInput) {
		t.Fatalf("sem id do provedor, veio %v", err)
	}
	if err := p.Approve("mp-1", p.Amount()-1, t0); !errors.Is(err, domainerrors.ErrAmountMismatch) {
		t.Fatalf("valor divergente, veio %v", err)
	}
	if err := p.Approve("mp-1", p.Amount(), t0); err != nil {
		t.Fatal(err)
	}
	if p.Status() != PaymentApproved || p.ProviderPaymentID() != "mp-1" || p.IsOpen() {
		t.Fatalf("aprovação não registrada: %+v", p.Snapshot())
	}
	if err := newPayment(t).Approve("mp-2", p.Amount(), t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("created não pode ir direto para approved, veio %v", err)
	}
}

func TestPayment_AttachProviderPayment(t *testing.T) {
	p := pendingPayment(t)
	if !p.AttachProviderPayment("mp-9", t0.Add(time.Minute)) || p.ProviderPaymentID() != "mp-9" {
		t.Fatal("deveria guardar o id em análise")
	}
	if p.AttachProviderPayment("mp-10", t0) {
		t.Fatal("não pode sobrescrever o id já guardado")
	}
	if newPayment(t).AttachProviderPayment("mp-1", t0) {
		t.Fatal("só pagamentos pending guardam id em análise")
	}
	if pendingPayment(t).AttachProviderPayment("", t0) {
		t.Fatal("id vazio ignorado")
	}
}

func TestPayment_Fail(t *testing.T) {
	p := pendingPayment(t)
	if err := p.Fail(" recusado ", "mp-1", t0); err != nil {
		t.Fatal(err)
	}
	if p.Status() != PaymentFailed || p.FailureReason() != "recusado" || p.ProviderPaymentID() != "mp-1" {
		t.Fatalf("falha não registrada: %+v", p.Snapshot())
	}
	if err := p.Fail("de novo", "", t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("failed é final, veio %v", err)
	}
	created := newPayment(t)
	if err := created.Fail("simulada", "", t0); err != nil || created.ProviderPaymentID() != "" {
		t.Fatalf("created → failed: %v", err)
	}
	// Falha não sobrescreve um id já guardado durante a análise.
	q := pendingPayment(t)
	q.AttachProviderPayment("mp-a", t0)
	_ = q.Fail("x", "mp-b", t0)
	if q.ProviderPaymentID() != "mp-a" {
		t.Fatalf("id sobrescrito: %s", q.ProviderPaymentID())
	}
}

func TestPayment_CancelERefund(t *testing.T) {
	p := pendingPayment(t)
	if err := p.Cancel(" orçamento cancelado ", t0); err != nil || p.Status() != PaymentCancelled || p.FailureReason() != "orçamento cancelado" {
		t.Fatalf("cancel: %v %+v", err, p.Snapshot())
	}
	if err := p.Refund(t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("só aprovado é estornável, veio %v", err)
	}
	a := pendingPayment(t)
	_ = a.Approve("mp-1", a.Amount(), t0)
	if err := a.Cancel("x", t0); !errors.Is(err, domainerrors.ErrInvalidTransition) {
		t.Fatalf("aprovado não se cancela, se estorna; veio %v", err)
	}
	later := t0.Add(time.Hour)
	if err := a.Refund(later); err != nil || a.Status() != PaymentRefunded || a.RefundedAt() == nil || !a.RefundedAt().Equal(later) {
		t.Fatalf("refund: %v %+v", err, a.Snapshot())
	}
}

func TestPayment_SnapshotRestore(t *testing.T) {
	p := pendingPayment(t)
	s := p.Snapshot()
	s.Version = 3
	r := RestorePayment(s)
	if r.ID() != p.ID() || r.Status() != PaymentPending || r.Version() != 3 || r.CheckoutURL() != p.CheckoutURL() ||
		r.ProviderPreferenceID() != "pref-1" || r.Provider() != ProviderFake {
		t.Fatalf("restore perdeu estado: %+v", r.Snapshot())
	}
}
