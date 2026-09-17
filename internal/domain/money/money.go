// Package money provides exact, immutable monetary values at a two-decimal scale.
package money

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	ErrInvalidAmount    = errors.New("invalid monetary amount")
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("monetary overflow")
	ErrInvalidMoney     = errors.New("uninitialized money")
)

// Money stores signed int64 minor units, ranging from -92233720368547758.08
// to 92233720368547758.07. Its zero value is invalid; use Zero instead.
type Money struct {
	minorUnits int64
	currency   string
}

// Parse accepts non-negative ASCII decimal amounts with an integer part and
// an optional one- or two-digit fraction. Leading zeros are normalized.
// Currency must contain three ASCII letters; registry membership is not checked.
func Parse(amount, currency string) (Money, error) {
	m, err := Zero(currency)
	if err != nil {
		return Money{}, err
	}
	whole, fraction, dot := strings.Cut(amount, ".")
	if whole == "" || (dot && (len(fraction) < 1 || len(fraction) > 2)) {
		return Money{}, ErrInvalidAmount
	}
	for _, part := range []string{whole, fraction} {
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				return Money{}, ErrInvalidAmount
			}
		}
	}
	// Append fractional padding before accumulation so every digit is checked
	// against the minor-unit limit, without an overflowing scale multiplication.
	digits := whole + fraction + strings.Repeat("0", 2-len(fraction))
	for i := 0; i < len(digits); i++ {
		digit := int64(digits[i] - '0')
		if m.minorUnits > (math.MaxInt64-digit)/10 {
			return Money{}, ErrOverflow
		}
		m.minorUnits = m.minorUnits*10 + digit
	}
	return m, nil
}

// Zero creates valid zero money in the supplied currency.
func Zero(currency string) (Money, error) {
	if len(currency) != 3 {
		return Money{}, ErrInvalidCurrency
	}
	for i := 0; i < len(currency); i++ {
		c := currency[i]
		if !(c >= 'A' && c <= 'Z') && !(c >= 'a' && c <= 'z') {
			return Money{}, ErrInvalidCurrency
		}
	}
	return Money{currency: strings.ToUpper(currency)}, nil
}

func (m Money) validate() error {
	if m.currency == "" {
		return ErrInvalidMoney
	}
	return nil
}

func (m Money) compatible(other Money) error {
	if err := m.validate(); err != nil {
		return err
	}
	if err := other.validate(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return ErrCurrencyMismatch
	}
	return nil
}

// Add returns the sum without modifying either operand.
func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: a + b, currency: m.currency}, nil
}

// Subtract returns the difference, which may be negative.
func (m Money) Subtract(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	a, b := m.minorUnits, other.minorUnits
	if (b > 0 && a < math.MinInt64+b) || (b < 0 && a > math.MaxInt64+b) {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: a - b, currency: m.currency}, nil
}

// Negate returns the additive inverse. The minimum int64 value has no inverse.
func (m Money) Negate() (Money, error) {
	if err := m.validate(); err != nil {
		return Money{}, err
	}
	if m.minorUnits == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minorUnits: -m.minorUnits, currency: m.currency}, nil
}

// Compare returns -1, 0, or 1 when m is less than, equal to, or greater than other.
func (m Money) Compare(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	if m.minorUnits < other.minorUnits {
		return -1, nil
	}
	if m.minorUnits > other.minorUnits {
		return 1, nil
	}
	return 0, nil
}

// Amount serializes the signed amount with exactly two fractional digits.
func (m Money) Amount() (string, error) {
	if err := m.validate(); err != nil {
		return "", err
	}
	whole, fraction := m.minorUnits/100, m.minorUnits%100
	if m.minorUnits < 0 {
		// Negate the quotient and remainder separately to handle MinInt64.
		return fmt.Sprintf("-%d.%02d", -whole, -fraction), nil
	}
	return fmt.Sprintf("%d.%02d", whole, fraction), nil
}

// Currency returns the normalized uppercase currency code.
func (m Money) Currency() (string, error) {
	if err := m.validate(); err != nil {
		return "", err
	}
	return m.currency, nil
}

// FromMinorUnits constructs exact signed money, including internal negative values.
func FromMinorUnits(minorUnits int64, currency string) (Money, error) {
	m, err := Zero(currency)
	if err != nil {
		return Money{}, err
	}
	m.minorUnits = minorUnits
	return m, nil
}

func (m Money) MinorUnits() (int64, error) {
	if err := m.validate(); err != nil {
		return 0, err
	}
	return m.minorUnits, nil
}
