package fakegateway

import (
	"context"
	"errors"
	"testing"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

func TestFakeGateway(t *testing.T) {
	g := New("http://localhost:8082/")
	ctx := context.Background()
	if g.Provider() != entities.ProviderFake {
		t.Fatal("provider")
	}
	pref, url, err := g.CreateCheckout(ctx, ports.CheckoutRequest{PaymentID: "p-1"})
	if err != nil || pref != "fake-pref-p-1" || url != "http://localhost:8082/fake-checkout/p-1" {
		t.Fatalf("checkout: %s %s %v", pref, url, err)
	}
	if _, err := g.GetPayment(ctx, "x"); !errors.Is(err, ports.ErrGatewayNotFound) {
		t.Fatalf("GetPayment: %v", err)
	}
	if g.Refund(ctx, "fake-p-1", "p-1") != nil || g.Cancel(ctx, "fake-p-1") != nil {
		t.Fatal("refund/cancel do fake sempre funcionam")
	}
}
