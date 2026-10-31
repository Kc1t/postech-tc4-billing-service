// Package fakegateway é o adapter fake da porta PaymentGateway (PAYMENT_GATEWAY=fake), para
// desenvolvimento local, testes BDD/componente e demonstração sem credenciais do Mercado Pago.
//
// O "checkout" é uma página local (GET /fake-checkout/{payment_id}) e o retorno do provedor é
// simulado pela API (POST /api/v1/payments/{id}/simulate), que passa pelas mesmas regras do
// webhook real.
package fakegateway

import (
	"context"
	"strings"

	"github.com/Kc1t/postech-tc4-billing-service/internal/domain/entities"
	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
	"github.com/Kc1t/postech-tc4-billing-service/pkg/logger"
)

// Gateway implementa ports.PaymentGateway sem rede.
type Gateway struct {
	checkoutBaseURL string
}

// New cria o gateway fake. checkoutBaseURL é a URL pública do billing (ex.: http://localhost:8082).
func New(checkoutBaseURL string) *Gateway {
	return &Gateway{checkoutBaseURL: strings.TrimRight(checkoutBaseURL, "/")}
}

var _ ports.PaymentGateway = (*Gateway)(nil)

func (g *Gateway) Provider() entities.Provider { return entities.ProviderFake }

func (g *Gateway) CreateCheckout(_ context.Context, req ports.CheckoutRequest) (string, string, error) {
	return "fake-pref-" + req.PaymentID, g.checkoutBaseURL + "/fake-checkout/" + req.PaymentID, nil
}

// GetPayment não tem o que consultar: no modo fake não há webhook; o simulador aplica o resultado.
func (g *Gateway) GetPayment(context.Context, string) (ports.ProviderPayment, error) {
	return ports.ProviderPayment{}, ports.ErrGatewayNotFound
}

func (g *Gateway) Refund(ctx context.Context, providerPaymentID, idempotencyKey string) error {
	logger.FromContext(ctx).Info("fake_gateway_refund", "provider_payment_id", providerPaymentID, "idempotency_key", idempotencyKey)
	return nil
}

func (g *Gateway) Cancel(ctx context.Context, providerPaymentID string) error {
	logger.FromContext(ctx).Info("fake_gateway_cancel", "provider_payment_id", providerPaymentID)
	return nil
}
