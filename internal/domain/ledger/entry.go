package ledger

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

var ErrInvalidEntry = errors.New("invalid ledger entry")

type Entry struct {
	id, walletID, transactionID string
	direction                   Direction
	amount, before, after       money.Money
	createdAt                   time.Time
}

func New(id, walletID, transactionID string, direction Direction, amount, before, after money.Money, at time.Time) (Entry, error) {
	for _, v := range []string{id, walletID, transactionID} {
		if v == "" || !utf8.ValidString(v) || strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return Entry{}, ErrInvalidEntry
		}
	}
	if at.IsZero() {
		return Entry{}, ErrInvalidEntry
	}
	c, err := amount.Currency()
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	zero, _ := money.Zero(c)
	for i, v := range []money.Money{amount, before, after} {
		cmp, err := v.Compare(zero)
		if err != nil {
			return Entry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
		}
		if cmp < 0 || (i == 0 && cmp == 0) {
			return Entry{}, ErrInvalidEntry
		}
	}
	var expected money.Money
	switch direction {
	case Debit:
		expected, err = before.Subtract(amount)
	case Credit:
		expected, err = before.Add(amount)
	default:
		return Entry{}, ErrInvalidEntry
	}
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	cmp, err := expected.Compare(after)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrInvalidEntry, err)
	}
	if cmp != 0 {
		return Entry{}, ErrInvalidEntry
	}
	return Entry{id, walletID, transactionID, direction, amount, before, after, at.UTC()}, nil
}
func (e Entry) ID() string                 { return e.id }
func (e Entry) WalletID() string           { return e.walletID }
func (e Entry) TransactionID() string      { return e.transactionID }
func (e Entry) Direction() Direction       { return e.direction }
func (e Entry) Money() money.Money         { return e.amount }
func (e Entry) BalanceBefore() money.Money { return e.before }
func (e Entry) BalanceAfter() money.Money  { return e.after }
func (e Entry) CreatedAt() time.Time       { return e.createdAt }
