package mercadopago

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/Kc1t/postech-tc4-billing-service/internal/ports"
)

// SignatureVerifier valida o header x-signature das notificações do Mercado Pago (aba "Sem SDK"
// da documentação de Webhooks):
//
//  1. separa x-signature por "," para obter ts (timestamp em ms) e v1;
//  2. monta o manifest "id:{data.id};request-id:{x-request-id};ts:{ts};" — data.id vem da query
//     string, em minúsculas; o par de data.id ou de x-request-id sai do manifest se faltar;
//  3. calcula HMAC-SHA256(secret, manifest) em hex e compara com v1 em tempo constante.
//
// Opcionalmente (MaxAge > 0) rejeita notificações cujo ts esteja fora da janela de tolerância,
// limitando a reutilização de uma notificação capturada.
type SignatureVerifier struct {
	secret []byte
	maxAge time.Duration
	now    func() time.Time
}

// NewSignatureVerifier cria o verificador com a assinatura secreta do painel (MP_WEBHOOK_SECRET).
// Sem secret, toda notificação é rejeitada (falha fechada).
func NewSignatureVerifier(secret string, maxAge time.Duration) *SignatureVerifier {
	return &SignatureVerifier{secret: []byte(secret), maxAge: maxAge, now: time.Now}
}

var _ ports.WebhookVerifier = (*SignatureVerifier)(nil)

// Verify confere a assinatura. signature é o header x-signature, requestID o x-request-id e
// dataID o parâmetro data.id da query string.
func (v *SignatureVerifier) Verify(signature, requestID, dataID string) bool {
	if len(v.secret) == 0 {
		return false
	}
	ts, v1 := parseSignature(signature)
	if ts == "" || v1 == "" {
		return false
	}
	if v.maxAge > 0 && !v.fresh(ts) {
		return false
	}
	expected := sign(v.secret, Manifest(dataID, requestID, ts))
	return hmac.Equal([]byte(expected), []byte(v1))
}

func (v *SignatureVerifier) fresh(ts string) bool {
	ms, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	age := v.now().Sub(time.UnixMilli(ms))
	return age <= v.maxAge && age >= -v.maxAge
}

func parseSignature(header string) (ts, v1 string) {
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "ts":
			ts = strings.TrimSpace(kv[1])
		case "v1":
			v1 = strings.TrimSpace(kv[1])
		}
	}
	return ts, v1
}

// Manifest monta o texto assinado pelo Mercado Pago.
func Manifest(dataID, requestID, ts string) string {
	var parts []string
	if id := strings.ToLower(dataID); id != "" {
		parts = append(parts, "id:"+id)
	}
	if requestID != "" {
		parts = append(parts, "request-id:"+requestID)
	}
	parts = append(parts, "ts:"+ts)
	return strings.Join(parts, ";") + ";"
}

// Sign devolve o header x-signature que o Mercado Pago enviaria para estes dados. Serve para
// testes e para simular notificações em desenvolvimento (collection do Postman).
func Sign(secret, dataID, requestID, ts string) string {
	return "ts=" + ts + ",v1=" + sign([]byte(secret), Manifest(dataID, requestID, ts))
}

func sign(secret []byte, manifest string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(manifest))
	return hex.EncodeToString(mac.Sum(nil))
}
