package entities

import "math"

// Valores monetários trafegam como float64 (contrato das mensagens e numeric(12,2) no banco),
// mas toda conta é feita em centavos inteiros para não acumular erro de ponto flutuante.

// toCents converte reais em centavos, arredondando para o centavo mais próximo.
func toCents(v float64) int64 { return int64(math.Round(v * 100)) }

// fromCents converte centavos em reais.
func fromCents(c int64) float64 { return float64(c) / 100 }

// RoundMoney arredonda um valor para duas casas decimais.
func RoundMoney(v float64) float64 { return fromCents(toCents(v)) }

// SameAmount compara dois valores no nível do centavo.
func SameAmount(a, b float64) bool { return toCents(a) == toCents(b) }
