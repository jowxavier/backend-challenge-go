package financial

import (
	"crypto/sha256"
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	wt "github.com/jowxavier/backend-challenge-go/internal/domain/wagertransaction"
	"testing"
)

func TestCanonicalPayload(t *testing.T) {
	for _, amount := range []string{"10.5", "0010.50", "10.50"} {
		t.Run(amount, func(t *testing.T) {
			m, err := money.Parse(amount, "brl")
			if err != nil {
				t.Fatal(err)
			}
			r := ProcessRequest{ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.BET, Money: m}
			want := `{"externalTransactionId":"e","gameId":"g","kind":"BET","money":{"amount":"10.50","currency":"BRL"},"playerId":"u","providerId":"p","referenceExternalTransactionId":null,"roundId":"r","walletId":"w"}`
			b, err := CanonicalPayload(r)
			if err != nil || string(b) != want {
				t.Fatalf("%s %v", b, err)
			}
			hash, err := Fingerprint(r)
			if err != nil || hash != sha256.Sum256([]byte(want)) {
				t.Fatal(hash, err)
			}
			r.IdempotencyKey = "different"
			other, _ := Fingerprint(r)
			if hash != other {
				t.Fatal("key included in hash")
			}
		})
	}
}
func TestFingerprintFields(t *testing.T) {
	m, _ := money.Parse("1", "BRL")
	base := ProcessRequest{ProviderID: "p", ExternalTransactionID: "e", PlayerID: "u", WalletID: "w", RoundID: "r", GameID: "g", Kind: wt.WIN, Money: m}
	hash, _ := Fingerprint(base)
	for _, tc := range []struct {
		name   string
		change func(*ProcessRequest)
	}{
		{"provider", func(r *ProcessRequest) { r.ProviderID = "other" }},
		{"external", func(r *ProcessRequest) { r.ExternalTransactionID = "other" }},
		{"player", func(r *ProcessRequest) { r.PlayerID = "other" }},
		{"wallet", func(r *ProcessRequest) { r.WalletID = "other" }},
		{"round", func(r *ProcessRequest) { r.RoundID = "other" }},
		{"game", func(r *ProcessRequest) { r.GameID = "other" }},
		{"kind", func(r *ProcessRequest) { r.Kind = wt.BET }},
		{"amount", func(r *ProcessRequest) { r.Money, _ = money.Parse("2", "BRL") }},
		{"currency", func(r *ProcessRequest) { r.Money, _ = money.Parse("1", "USD") }},
		{"reference", func(r *ProcessRequest) { r.ReferenceExternalTransactionID = "ref" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.change(&r)
			got, err := Fingerprint(r)
			if err != nil || got == hash {
				t.Fatal(got, err)
			}
		})
	}
	base.GameID = "<>&\"\\é"
	b, err := CanonicalPayload(base)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"externalTransactionId":"e","gameId":"\u003c\u003e\u0026\"\\é","kind":"WIN","money":{"amount":"1.00","currency":"BRL"},"playerId":"u","providerId":"p","referenceExternalTransactionId":null,"roundId":"r","walletId":"w"}`
	if string(b) != want {
		t.Fatalf("escaping: %s", b)
	}
	if _, err := Fingerprint(ProcessRequest{}); err == nil {
		t.Fatal("uninitialized money accepted")
	}
}
