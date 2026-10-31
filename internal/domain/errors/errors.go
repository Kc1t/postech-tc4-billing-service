// Package domainerrors reúne os erros sentinela do domínio de orçamento e pagamento.
//
// Os adapters traduzem esses erros: o HTTP vira status + código padronizado, o consumidor de
// mensagens decide entre evento de falha (regra de negócio) e nova tentativa (falha transitória).
// Sempre compare com errors.Is: os casos de uso embrulham os sentinelas com contexto.
package domainerrors

import "errors"

var (
	// ErrBudgetNotFound indica orçamento inexistente.
	ErrBudgetNotFound = errors.New("orçamento não encontrado")
	// ErrPaymentNotFound indica pagamento inexistente.
	ErrPaymentNotFound = errors.New("pagamento não encontrado")
	// ErrBudgetAlreadyExists indica que a OS já tem orçamento (os_id é único).
	ErrBudgetAlreadyExists = errors.New("a ordem de serviço já possui orçamento")
	// ErrInvalidBudget indica dados de orçamento que violam as regras de criação.
	ErrInvalidBudget = errors.New("orçamento inválido")
	// ErrInvalidTransition indica mudança de status não permitida pela máquina de estados.
	ErrInvalidTransition = errors.New("transição de status inválida")
	// ErrBudgetNotApproved indica tentativa de cobrar um orçamento que não está aprovado.
	ErrBudgetNotApproved = errors.New("orçamento não está aprovado")
	// ErrAmountMismatch indica valor pago diferente do valor cobrado.
	ErrAmountMismatch = errors.New("valor pago diverge do valor do orçamento")
	// ErrConcurrentUpdate indica conflito de optimistic lock (version mudou entre a leitura e a escrita).
	ErrConcurrentUpdate = errors.New("registro alterado por outra operação; tente novamente")
	// ErrNotOwner indica cliente acessando orçamento ou pagamento de outro documento.
	ErrNotOwner = errors.New("o recurso pertence a outro cliente")
	// ErrInvalidInput indica entrada malformada vinda de quem chama o caso de uso.
	ErrInvalidInput = errors.New("entrada inválida")
	// ErrSimulationDisabled indica uso do simulador de pagamento fora do gateway fake.
	ErrSimulationDisabled = errors.New("simulação de pagamento disponível apenas com PAYMENT_GATEWAY=fake")
)
