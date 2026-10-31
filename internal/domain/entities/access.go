package entities

import domainerrors "github.com/Kc1t/postech-tc4-billing-service/internal/domain/errors"

// CheckReadAccess aplica a regra de posse na consulta: a administração vê todos os orçamentos
// (e seus pagamentos); o cliente, só os emitidos para o próprio documento (claim "document" do
// JWT emitido pela Lambda de autenticação por CPF). Mecânico não consulta cobrança.
func (b *Budget) CheckReadAccess(a Actor) error {
	if a.IsAdmin() || a.Owns(b.requester.Document) {
		return nil
	}
	return domainerrors.ErrNotOwner
}

// CheckDecisionAccess aplica a regra de posse na decisão: só o cliente dono aprova ou recusa.
func (b *Budget) CheckDecisionAccess(a Actor) error {
	if a.Owns(b.requester.Document) {
		return nil
	}
	return domainerrors.ErrNotOwner
}
