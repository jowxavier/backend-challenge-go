package wagertransaction

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"time"
)

func NewOpening(id, playerID, walletID string, m money.Money, at time.Time) (*WagerTransaction, error) {
	return rehydrateOpening(State{ExternalInput: ExternalInput{ID: id, PlayerID: playerID, WalletID: walletID, Kind: OPENING, Money: m}, Status: PROCESSED, CreatedAt: at.UTC(), UpdatedAt: at.UTC()})
}
func rehydrateOpening(s State) (*WagerTransaction, error) {
	if !validID(s.ID) || !validID(s.PlayerID) || !validID(s.WalletID) {
		return nil, ErrInvalidField
	}
	if s.ProviderID != "" || s.ExternalTransactionID != "" || s.RoundID != "" || s.GameID != "" || s.ReferenceExternalTransactionID != "" {
		return nil, ErrInvalidField
	}
	if s.Status != PROCESSED || s.FailureCode != "" {
		return nil, ErrInvalidTransaction
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, ErrInvalidTime
	}
	n, err := s.Money.MinorUnits()
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, ErrInvalidAmount
	}
	return &WagerTransaction{id: s.ID, playerID: s.PlayerID, walletID: s.WalletID, kind: OPENING, money: s.Money, status: PROCESSED, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}, nil
}
