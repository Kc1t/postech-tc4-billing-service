package presenter

import (
	"testing"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

func TestBudgetEPayment(t *testing.T) {
	now := time.Now().UTC()
	b, err := entities.NewBudget(entities.NewBudgetInput{
		OSID: "os-1", OSCode: 9, SagaID: "s",
		Requester: entities.Requester{ID: "r", Name: "Ana", Email: "a@x.com", Document: "52998224725"},
		Items: []entities.BudgetItem{
			{Kind: entities.ItemService, Ref: "1", Description: "Mão de obra", Quantity: 1, UnitPrice: 100},
			{Kind: entities.ItemPart, Ref: "P", Description: "Peça", Quantity: 3, UnitPrice: 33.33},
		},
		Total: 199.99,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if r := View(ports.BudgetView{Budget: b}); r.Payment != nil || r.Total != 199.99 || r.Items[1].Subtotal != 99.99 || r.Requester.Name != "Ana" {
		t.Fatalf("sem pagamento: %+v", r)
	}

	_ = b.Approve(now)
	p, _ := entities.NewPayment(b, entities.ProviderMercadoPago, now)
	_ = p.LinkCreated("pref", "https://mp/1", now)
	r := Budget(b, p)
	if r.Status != "approved" || r.DecidedAt == nil || r.Payment == nil || r.Payment.CheckoutURL != "https://mp/1" ||
		r.Payment.Provider != "mercadopago" || r.Payment.Status != "pending" || r.Payment.BudgetID != b.ID() {
		t.Fatalf("com pagamento: %+v", r)
	}
}
