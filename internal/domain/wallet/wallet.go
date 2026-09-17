// Package wallet implements the wallet aggregate's in-memory balance invariants.
package wallet

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

var (
	ErrInvalidWalletID       = errors.New("invalid wallet ID")
	ErrInvalidPlayerID       = errors.New("invalid player ID")
	ErrInvalidInitialBalance = errors.New("invalid initial balance")
	ErrInvalidAmount         = errors.New("invalid wallet amount")
	ErrInsufficientBalance   = errors.New("insufficient balance")
	ErrInvalidWallet         = errors.New("uninitialized wallet")
	ErrInvalidTime           = errors.New("invalid wallet timestamp")
	ErrVersionOverflow       = errors.New("wallet version overflow")
)

// Wallet is a mutable aggregate with private state. Financial operations
// validate and calculate completely before changing any fields.
// The zero value is invalid for financial operations.
type Wallet struct {
	id, playerID, currency string
	balance                money.Money
	version                int64
	createdAt, updatedAt   time.Time
}

// New creates a wallet at version 1. IDs are opaque non-empty strings without
// whitespace or control characters. Time is supplied by the caller and stored in UTC.
func New(id, playerID, currency string, initialBalance money.Money, at time.Time) (Wallet, error) {
	if !validID(id) {
		return Wallet{}, ErrInvalidWalletID
	}
	if !validID(playerID) {
		return Wallet{}, ErrInvalidPlayerID
	}
	zero, err := money.Zero(currency)
	if err != nil {
		return Wallet{}, err
	}
	cmp, err := initialBalance.Compare(zero)
	if err != nil {
		return Wallet{}, fmt.Errorf("%w: %w", ErrInvalidInitialBalance, err)
	}
	if cmp < 0 {
		return Wallet{}, ErrInvalidInitialBalance
	}
	if at.IsZero() {
		return Wallet{}, ErrInvalidTime
	}
	normalized, err := zero.Currency()
	if err != nil {
		return Wallet{}, err
	}
	return Wallet{id: id, playerID: playerID, currency: normalized, balance: initialBalance,
		version: 1, createdAt: at.UTC(), updatedAt: at.UTC()}, nil
}

func validID(id string) bool {
	return id != "" && utf8.ValidString(id) && strings.IndexFunc(id, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) == -1
}

func (w Wallet) ID() string           { return w.id }
func (w Wallet) PlayerID() string     { return w.playerID }
func (w Wallet) Currency() string     { return w.currency }
func (w Wallet) Balance() money.Money { return w.balance }
func (w Wallet) Version() int64       { return w.version }
func (w Wallet) CreatedAt() time.Time { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }

// Credit adds the amount after validation. A valid zero amount is a no-op.
func (w *Wallet) Credit(amount money.Money, at time.Time) error {
	return w.change(amount, at, false)
}

// Debit rejects insufficient funds without changing the wallet.
func (w *Wallet) Debit(amount money.Money, at time.Time) error {
	return w.change(amount, at, true)
}

func (w *Wallet) change(amount money.Money, at time.Time, debit bool) error {
	if w == nil || w.version == 0 {
		return ErrInvalidWallet
	}
	zero, err := money.Zero(w.currency)
	if err != nil {
		return err
	}
	cmp, err := amount.Compare(zero)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	if cmp < 0 {
		return ErrInvalidAmount
	}
	if cmp == 0 {
		return nil
	}
	if at.IsZero() {
		return ErrInvalidTime
	}
	var balance money.Money
	if debit {
		cmp, err = w.balance.Compare(amount)
		if err != nil {
			return err
		}
		if cmp < 0 {
			return ErrInsufficientBalance
		}
		balance, err = w.balance.Subtract(amount)
	} else {
		balance, err = w.balance.Add(amount)
	}
	if err != nil {
		return err
	}
	if w.version == math.MaxInt64 {
		return ErrVersionOverflow
	}
	w.balance = balance
	w.version++
	w.updatedAt = at.UTC()
	return nil
}
