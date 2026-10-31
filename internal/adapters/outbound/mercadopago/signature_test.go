package mercadopago

import (
	"strconv"
	"testing"
	"time"
)

const secret = "segredo-do-painel"

func TestManifest(t *testing.T) {
	cases := []struct {
		dataID, requestID, ts, want string
	}{
		{"123456", "bb56a2f1", "1742505638683", "id:123456;request-id:bb56a2f1;ts:1742505638683;"},
		{"ORD01ABC", "r", "1", "id:ord01abc;request-id:r;ts:1;"}, // alfanumérico vai em minúsculas
		{"", "r", "1", "request-id:r;ts:1;"},                     // sem data.id: o par sai
		{"9", "", "1", "id:9;ts:1;"},                             // sem x-request-id: o par sai
	}
	for _, c := range cases {
		if got := Manifest(c.dataID, c.requestID, c.ts); got != c.want {
			t.Errorf("Manifest(%q,%q,%q) = %q, esperava %q", c.dataID, c.requestID, c.ts, got, c.want)
		}
	}
}

// Vetor calculado à parte (python3 hmac): HMAC-SHA256("segredo-do-painel", "id:123456;request-id:bb56a2f1-6aae-46ac-982e-9dcd3581d08e;ts:1742505638683;").
const knownV1 = "56773a1d59054a5501f8d7ea1659b0ef15b764174a5e0992302d14f6f29ae9b8"

func TestVerify(t *testing.T) {
	v := NewSignatureVerifier(secret, 0)
	const rid, ts = "bb56a2f1-6aae-46ac-982e-9dcd3581d08e", "1742505638683"
	valid := Sign(secret, "123456", rid, ts)

	if valid != "ts="+ts+",v1="+knownV1 {
		t.Fatalf("Sign divergiu do vetor conhecido: %s", valid)
	}
	cases := map[string]struct {
		sig, rid, id string
		want         bool
	}{
		"válida":                     {valid, rid, "123456", true},
		"válida com espaços":         {" ts=" + ts + " , v1=" + knownV1 + " ", rid, "123456", true},
		"ordem invertida no header":  {"v1=" + knownV1 + ",ts=" + ts, rid, "123456", true},
		"data.id adulterado":         {valid, rid, "123457", false},
		"request-id adulterado":      {valid, "outro", "123456", false},
		"v1 adulterado":              {"ts=" + ts + ",v1=" + knownV1[:63] + "0", rid, "123456", false},
		"sem ts":                     {"v1=" + knownV1, rid, "123456", false},
		"sem v1":                     {"ts=" + ts, rid, "123456", false},
		"header vazio":               {"", rid, "123456", false},
		"lixo":                       {"abc,=,ts", rid, "123456", false},
		"segredo diferente":          {Sign("outro-segredo", "123456", rid, ts), rid, "123456", false},
		"maiúsculas no id da query":  {Sign(secret, "abc", rid, ts), rid, "ABC", true},
		"sem request-id dos 2 lados": {Sign(secret, "123456", "", ts), "", "123456", true},
	}
	for name, c := range cases {
		if got := v.Verify(c.sig, c.rid, c.id); got != c.want {
			t.Errorf("%s: Verify = %v, esperava %v", name, got, c.want)
		}
	}
}

func TestVerify_SemSegredoFalhaFechado(t *testing.T) {
	if NewSignatureVerifier("", 0).Verify(Sign("", "1", "r", "1"), "r", "1") {
		t.Fatal("sem secret configurado nenhuma notificação pode passar")
	}
}

func TestVerify_JanelaDeTolerancia(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	v := NewSignatureVerifier(secret, 5*time.Minute)
	v.now = func() time.Time { return now }
	ts := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).UnixMilli(), 10) }

	if !v.Verify(Sign(secret, "1", "r", ts(-time.Minute)), "r", "1") {
		t.Error("dentro da janela deveria passar")
	}
	if v.Verify(Sign(secret, "1", "r", ts(-10*time.Minute)), "r", "1") {
		t.Error("notificação antiga deveria ser rejeitada")
	}
	if v.Verify(Sign(secret, "1", "r", ts(10*time.Minute)), "r", "1") {
		t.Error("ts no futuro além da janela deveria ser rejeitado")
	}
	if v.Verify(Sign(secret, "1", "r", "agora"), "r", "1") {
		t.Error("ts não numérico deveria ser rejeitado")
	}
}
