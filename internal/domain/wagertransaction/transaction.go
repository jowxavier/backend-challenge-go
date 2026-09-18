// Package wagertransaction models external operation validation and lifecycle.
package wagertransaction

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
)

type Kind string

const (
	BET      Kind = "BET"
	WIN      Kind = "WIN"
	LOSS     Kind = "LOSS"
	REFUND   Kind = "REFUND"
	ROLLBACK Kind = "ROLLBACK"
)

type Status string

const (
	PENDING           Status = "PENDING"
	PENDING_REFERENCE Status = "PENDING_REFERENCE"
	PROCESSED         Status = "PROCESSED"
	REJECTED          Status = "REJECTED"
	FAILED            Status = "FAILED"
)

// FailureCode records an outcome, independently of errors returned by methods.
type FailureCode string

const (
	CurrencyMismatch               FailureCode = "CURRENCY_MISMATCH"
	MonetaryOverflow               FailureCode = "MONETARY_OVERFLOW"
	BetInsufficientFunds           FailureCode = "BET_INSUFFICIENT_FUNDS"
	ReversalInsufficientFunds      FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	ReferenceNotFound              FailureCode = "REFERENCE_NOT_FOUND"
	PermanentInfrastructureFailure FailureCode = "PERMANENT_INFRASTRUCTURE_FAILURE"
)

var (
	ErrInvalidTransaction = errors.New("uninitialized transaction")
	ErrInvalidField       = errors.New("invalid transaction field")
	ErrInvalidKind        = errors.New("invalid transaction kind")
	ErrInvalidAmount      = errors.New("invalid transaction amount")
	ErrInvalidReference   = errors.New("invalid transaction reference")
	ErrInvalidTransition  = errors.New("invalid transaction transition")
	ErrInvalidFailureCode = errors.New("invalid transaction failure code")
	ErrInvalidTime        = errors.New("invalid transaction timestamp")
)

// ExternalInput is copied during construction; it is not retained by reference.
type ExternalInput struct {
	ID, ProviderID, ExternalTransactionID string
	PlayerID, WalletID, RoundID, GameID   string
	Kind                                  Kind
	Money                                 money.Money
	ReferenceExternalTransactionID        string
}

type WagerTransaction struct {
	id, providerID, externalTransactionID string
	playerID, walletID, roundID, gameID   string
	kind                                  Kind
	money                                 money.Money
	referenceExternalTransactionID        string
	status                                Status
	failureCode                           FailureCode
	createdAt, updatedAt                  time.Time
}

func NewExternal(input ExternalInput, at time.Time) (*WagerTransaction, error) {
	return Rehydrate(State{ExternalInput: input, Status: PENDING, CreatedAt: at.UTC(), UpdatedAt: at.UTC()})
}

// State contains only persisted local state, not resolved references or wallet effects.
type State struct {
	ExternalInput
	Status               Status
	FailureCode          FailureCode
	CreatedAt, UpdatedAt time.Time
}

func Rehydrate(s State) (*WagerTransaction, error) {
	input := s.ExternalInput
	for _, field := range []struct{ name, value string }{
		{"id", input.ID}, {"providerId", input.ProviderID}, {"externalTransactionId", input.ExternalTransactionID},
		{"playerId", input.PlayerID}, {"walletId", input.WalletID}, {"roundId", input.RoundID}, {"gameId", input.GameID},
	} {
		if !validID(field.value) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidField, field.name)
		}
	}
	switch input.Kind {
	case BET, WIN, LOSS, REFUND, ROLLBACK:
	default:
		return nil, ErrInvalidKind
	}
	currency, err := input.Money.Currency()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	zero, err := money.Zero(currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	cmp, err := input.Money.Compare(zero)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	if (input.Kind == LOSS && cmp != 0) || (input.Kind != LOSS && cmp <= 0) {
		return nil, ErrInvalidAmount
	}
	ref := input.ReferenceExternalTransactionID
	if ref != "" && (!validID(ref) || ref == input.ExternalTransactionID) {
		return nil, ErrInvalidReference
	}
	if ((input.Kind == REFUND || input.Kind == ROLLBACK) && ref == "") || ((input.Kind == BET || input.Kind == LOSS) && ref != "") {
		return nil, ErrInvalidReference
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return nil, ErrInvalidTime
	}
	switch s.Status {
	case PENDING, PROCESSED:
		if s.FailureCode != "" {
			return nil, ErrInvalidFailureCode
		}
	case PENDING_REFERENCE:
		if ref == "" {
			return nil, ErrInvalidReference
		}
		if s.FailureCode != "" {
			return nil, ErrInvalidFailureCode
		}
	case REJECTED:
		if !businessCode(s.FailureCode) {
			return nil, ErrInvalidFailureCode
		}
	case FAILED:
		if s.FailureCode != PermanentInfrastructureFailure {
			return nil, ErrInvalidFailureCode
		}
	default:
		return nil, ErrInvalidTransaction
	}
	return &WagerTransaction{
		id: input.ID, providerID: input.ProviderID, externalTransactionID: input.ExternalTransactionID,
		playerID: input.PlayerID, walletID: input.WalletID, roundID: input.RoundID, gameID: input.GameID,
		kind: input.Kind, money: input.Money, referenceExternalTransactionID: ref,
		status: s.Status, failureCode: s.FailureCode, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt,
	}, nil
}

func validID(id string) bool {
	return id != "" && utf8.ValidString(id) && strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) == -1
}

func (t WagerTransaction) ID() string                    { return t.id }
func (t WagerTransaction) ProviderID() string            { return t.providerID }
func (t WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }
func (t WagerTransaction) PlayerID() string              { return t.playerID }
func (t WagerTransaction) WalletID() string              { return t.walletID }
func (t WagerTransaction) RoundID() string               { return t.roundID }
func (t WagerTransaction) GameID() string                { return t.gameID }
func (t WagerTransaction) Kind() Kind                    { return t.kind }
func (t WagerTransaction) Money() money.Money            { return t.money }
func (t WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}
func (t WagerTransaction) Status() Status           { return t.status }
func (t WagerTransaction) FailureCode() FailureCode { return t.failureCode }
func (t WagerTransaction) CreatedAt() time.Time     { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time     { return t.updatedAt }

func (t *WagerTransaction) WaitForReference(at time.Time) error {
	return t.transition(PENDING_REFERENCE, "", at)
}

// MarkProcessed validates lifecycle only, not wallet effects or reference eligibility.
func (t *WagerTransaction) MarkProcessed(at time.Time) error {
	return t.transition(PROCESSED, "", at)
}
func (t *WagerTransaction) Reject(code FailureCode, at time.Time) error {
	return t.transition(REJECTED, code, at)
}
func (t *WagerTransaction) Fail(code FailureCode, at time.Time) error {
	return t.transition(FAILED, code, at)
}

func (t *WagerTransaction) transition(next Status, code FailureCode, at time.Time) error {
	if t == nil || t.status == "" {
		return ErrInvalidTransaction
	}
	if t.status != PENDING && t.status != PENDING_REFERENCE {
		return ErrInvalidTransition
	}
	switch next {
	case PENDING_REFERENCE:
		if t.status != PENDING {
			return ErrInvalidTransition
		}
		if t.referenceExternalTransactionID == "" {
			return ErrInvalidReference
		}
	case PROCESSED:
	case REJECTED:
		switch code {
		case BetInsufficientFunds, ReversalInsufficientFunds, ReferenceNotFound, CurrencyMismatch, MonetaryOverflow:
		default:
			return ErrInvalidFailureCode
		}
	case FAILED:
		if code != PermanentInfrastructureFailure {
			return ErrInvalidFailureCode
		}
	default:
		return ErrInvalidTransition
	}
	if at.IsZero() {
		return ErrInvalidTime
	}
	t.status = next
	t.failureCode = code
	t.updatedAt = at.UTC()
	return nil
}

func businessCode(code FailureCode) bool {
	switch code {
	case BetInsufficientFunds, ReversalInsufficientFunds, ReferenceNotFound, CurrencyMismatch, MonetaryOverflow:
		return true
	default:
		return false
	}
}
