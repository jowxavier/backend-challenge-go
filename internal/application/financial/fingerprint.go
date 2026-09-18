package financial

import (
	"crypto/sha256"
	"encoding/json"
)

// CanonicalPayload uses encoding/json's sorted map keys, UTF-8 encoding and
// standard HTML escaping, with no trailing newline. An absent reference is null.
func CanonicalPayload(r ProcessRequest) ([]byte, error) {
	amount, err := r.Money.Amount()
	if err != nil {
		return nil, err
	}
	currency, err := r.Money.Currency()
	if err != nil {
		return nil, err
	}
	var reference any
	if r.ReferenceExternalTransactionID != "" {
		reference = r.ReferenceExternalTransactionID
	}
	return json.Marshal(map[string]any{
		"providerId": r.ProviderID, "externalTransactionId": r.ExternalTransactionID,
		"playerId": r.PlayerID, "walletId": r.WalletID, "roundId": r.RoundID,
		"gameId": r.GameID, "kind": r.Kind,
		"money":                          map[string]string{"amount": amount, "currency": currency},
		"referenceExternalTransactionId": reference,
	})
}

func Fingerprint(r ProcessRequest) ([32]byte, error) {
	b, err := CanonicalPayload(r)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}
