//go:build integration && oidcintegration

package postgres

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/application/financial"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"github.com/jowxavier/backend-challenge-go/internal/infrastructure/oidcauth"
	httpapi "github.com/jowxavier/backend-challenge-go/internal/interfaces/http"
)

func TestKeycloakHTTPSmoke(t *testing.T) {
	issuer := os.Getenv("TEST_OIDC_ISSUER")
	if issuer == "" {
		t.Fatal("TEST_OIDC_ISSUER required")
	}
	ctx := testContext(t)
	verifier, err := oidcauth.New(ctx, config.OIDCConfig{Issuer: issuer, Audience: "wager-api", InternalClient: "wallet-internal", ProviderClients: map[string]string{"provider-a": "provider-a", "provider-b": "provider-b"}})
	must(t, err)
	token := func(client string) string {
		req, err := http.NewRequestWithContext(ctx, "POST", issuer+"/protocol/openid-connect/token", strings.NewReader(url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {"local-" + client + "-secret"}}.Encode()))
		must(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("token endpoint: %d", res.StatusCode)
		}
		var result struct {
			Token string `json:"access_token"`
		}
		must(t, json.NewDecoder(res.Body).Decode(&result))
		return result.Token
	}
	internal, a, b := token("wallet-internal"), token("provider-a"), token("provider-b")
	pool := testPool(t)
	svc := financial.NewWalletService(NewWalletAPIStore(pool), time.Now)
	handler := httpapi.NewAPI(newProcessor(t, NewRunner(pool)), svc, NewWagerTransactionRepository(pool), verifier, healthy{}).Handler()
	server := httptest.NewServer(handler)
	defer server.Close()
	call := func(method, path, bearer, key, body string, status int) map[string]json.RawMessage {
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		must(t, err)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Idempotency-Key", key)
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		must(t, err)
		if res.StatusCode != status {
			t.Fatalf("HTTP %s got %d: %s", path, res.StatusCode, raw)
		}
		var out map[string]json.RawMessage
		must(t, json.Unmarshal(raw, &out))
		return out
	}
	w := call("POST", "/wallets", internal, "", `{"playerId":"smoke","initialBalance":{"amount":"100.00","currency":"BRL"}}`, 201)
	id := value(t, w, "id")
	request := `{"providerId":"provider-a","externalTransactionId":"bet","playerId":"smoke","walletId":"` + id + `","roundId":"r","gameId":"g","kind":"BET","money":{"amount":"80.00","currency":"BRL"}}`
	result := call("POST", "/wagering/transactions", a, "key", request, 200)
	tid := value(t, result, "transactionId")
	call("GET", "/wagering/transactions/"+tid, b, "", "", 404)
	call("POST", "/wagering/transactions", b, "attack", request, 403)
	call("GET", "/wallets/"+id, a, "", "", 403)
	call("POST", "/wagering/transactions", a, "key", request, 200)
	if count(t, pool, "wallet_ledger_entries") != 2 || count(t, pool, "outbox_events") != 4 {
		t.Fatal("smoke atomic effects")
	}
	badAudience, err := oidcauth.New(context.Background(), config.OIDCConfig{Issuer: issuer, Audience: "different", ProviderClients: map[string]string{"provider-a": "provider-a"}})
	must(t, err)
	if _, err = badAudience.Verify(ctx, a); err == nil {
		t.Fatal("wrong audience accepted")
	}
}
