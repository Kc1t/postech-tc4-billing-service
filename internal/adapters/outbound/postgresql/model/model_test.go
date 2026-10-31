package model

import (
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
)

var now = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func TestBudgetRoundTrip(t *testing.T) {
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 3, SagaID: "s-1",
		Requester: entities.Requester{ID: "r", Name: "Ana", Email: "a@x.com", Document: "52998224725"},
		Items:     []entities.BudgetItem{{Kind: entities.ItemPart, Ref: "P1", Description: "Peça", Quantity: 2, UnitPrice: 10.5}},
		Total:     21,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Approve(now)
	m := FromBudget(b)
	if m.TableName() != "budgets" || m.Items[0].Ref != "P1" || m.RequesterDocument != "52998224725" || m.Status != "approved" {
		t.Fatalf("FromBudget: %+v", m)
	}
	back := m.ToDomain().Snapshot()
	orig := b.Snapshot()
	if back.ID != orig.ID || back.Total != orig.Total || back.Items[0] != orig.Items[0] || back.Requester != orig.Requester ||
		back.Status != orig.Status || !back.DecidedAt.Equal(*orig.DecidedAt) || back.Version != orig.Version {
		t.Fatalf("round-trip: %+v != %+v", back, orig)
	}
}

func TestPaymentRoundTrip(t *testing.T) {
	b, _ := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 3, SagaID: "s-1", Requester: entities.Requester{Document: "52998224725"},
		Items: []entities.BudgetItem{{Kind: entities.ItemService, Ref: "1", Quantity: 1, UnitPrice: 5}}, Total: 5,
	}, now)
	_ = b.Approve(now)
	p, _ := entities.NewPayment(b, entities.ProviderMercadoPago, now)

	m := FromPayment(p)
	if m.TableName() != "payments" || m.ProviderPaymentID != nil || m.ProviderPreferenceID != nil || m.CheckoutURL != nil {
		t.Fatalf("campos vazios deveriam virar NULL: %+v", m)
	}
	if got := m.ToDomain(); got.ProviderPaymentID() != "" || got.RefundedAt() != nil {
		t.Fatalf("NULL deveria virar vazio: %+v", got.Snapshot())
	}

	_ = p.LinkCreated("pref", "https://mp/1", now)
	_ = p.Approve("mp-1", 5, now)
	_ = p.Refund(now)
	m = FromPayment(p)
	if *m.ProviderPaymentID != "mp-1" || *m.CheckoutURL != "https://mp/1" || *m.ProviderPreferenceID != "pref" {
		t.Fatalf("FromPayment: %+v", m)
	}
	back := m.ToDomain()
	if back.ProviderPaymentID() != "mp-1" || back.Status() != entities.PaymentRefunded || !back.RefundedAt().Equal(now) || back.Provider() != entities.ProviderMercadoPago {
		t.Fatalf("round-trip: %+v", back.Snapshot())
	}
}
