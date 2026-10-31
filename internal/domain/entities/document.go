package entities

import (
	"strings"
	"unicode"
)

// NormalizeDocument mantém só os dígitos de um CPF/CNPJ ("529.982.247-25" → "52998224725").
func NormalizeDocument(doc string) string {
	var b strings.Builder
	b.Grow(len(doc))
	for _, r := range doc {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// MaskDocument devolve o documento mascarado para logs: só os dois últimos dígitos ficam
// visíveis ("***.***.***-25" para CPF, "**.***.***/****-90" para CNPJ). Nunca logue o documento
// completo.
func MaskDocument(doc string) string {
	d := NormalizeDocument(doc)
	switch {
	case len(d) <= 2:
		return "***"
	case len(d) == 11:
		return "***.***.***-" + d[9:]
	case len(d) == 14:
		return "**.***.***/****-" + d[12:]
	default:
		return strings.Repeat("*", len(d)-2) + d[len(d)-2:]
	}
}
