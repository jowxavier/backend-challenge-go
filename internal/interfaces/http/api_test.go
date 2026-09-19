package http

import (
	"context"
	"github.com/jowxavier/backend-challenge-go/internal/application/access"
	"net/http/httptest"
	"strings"
	"testing"
)

type verifierFunc func(context.Context, string) (access.Principal, error)

func (f verifierFunc) Verify(ctx context.Context, s string) (access.Principal, error) {
	return f(ctx, s)
}

type readyFunc func(context.Context) error

func (f readyFunc) Check(ctx context.Context) error { return f(ctx) }
func TestAuthBoundary(t *testing.T) {
	api := NewAPI(nil, nil, nil, verifierFunc(func(_ context.Context, s string) (access.Principal, error) {
		if s == "valid" {
			return access.Principal{ProviderID: "a"}, nil
		}
		return access.Principal{}, access.ErrUnauthenticated
	}), readyFunc(func(context.Context) error { return nil }))
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{{"GET", "/health/live", "", "", 200}, {"GET", "/health/ready", "", "", 200}, {"POST", "/wagering/transactions", "", "{}", 401}, {"POST", "/wagering/transactions", "Bearer bad", "{}", 401}, {"POST", "/wagering/transactions", "Bearer valid", `{"providerId":"b"}`, 403}, {"GET", "/providers/b/wagering/transactions/x", "Bearer valid", "", 404}, {"POST", "/wallets", "Bearer valid", "{}", 403}, {"POST", "/wagering/transactions", "Bearer valid", "bad", 400}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", tc.token)
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
}
