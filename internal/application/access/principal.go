package access

import (
	"context"
	"errors"
)

var ErrUnauthenticated = errors.New("unauthenticated")

type Principal struct {
	ProviderID string
	Internal   bool
}
type Verifier interface {
	Verify(context.Context, string) (Principal, error)
}
