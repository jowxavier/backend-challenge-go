package wagertransaction

import (
	"github.com/jowxavier/backend-challenge-go/internal/domain/money"
	"testing"
	"time"
)

func TestOpening(t *testing.T) {
	at := time.Now()
	m, _ := money.Parse("1", "BRL")
	o, err := NewOpening("o", "p", "w", m, at)
	if err != nil || o.Status() != PROCESSED || o.ProviderID() != "" {
		t.Fatal(o, err)
	}
	for _, tc := range []struct {
		id string
		n  int64
	}{{"", 1}, {"o", 0}, {"o", -1}} {
		m, _ := money.FromMinorUnits(tc.n, "BRL")
		if _, err := NewOpening(tc.id, "p", "w", m, at); err == nil {
			t.Fatal(tc)
		}
	}
	if _, err = NewExternal(ExternalInput{Kind: OPENING}, at); err != ErrInvalidKind {
		t.Fatal(err)
	}
	if err = o.MarkProcessed(at); err == nil {
		t.Fatal("terminal transitioned")
	}
}
