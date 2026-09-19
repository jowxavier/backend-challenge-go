package oidcauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jowxavier/backend-challenge-go/internal/config"
)

func TestVerifiedTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/keys" {
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "test", "use": "sig", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	}))
	defer server.Close()
	issuer = server.URL
	v, err := New(context.Background(), config.OIDCConfig{Issuer: issuer, Audience: "api", InternalClient: "internal", ProviderClients: map[string]string{"a": "provider-a"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid", "internal", "malformed", "signature", "expired", "issuer", "audience", "future", "client", "unsigned"} {
		t.Run(name, func(t *testing.T) {
			claims := map[string]any{"iss": issuer, "aud": "api", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "azp": "a", "sub": "service-account-a"}
			signing := key
			switch name {
			case "signature":
				signing = other
			case "expired":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "issuer":
				claims["iss"] = "https://other"
			case "audience":
				claims["aud"] = "other"
			case "future":
				claims["nbf"] = time.Now().Add(time.Hour).Unix()
			case "client":
				claims["azp"] = "unknown"
			case "internal":
				claims["azp"] = "internal"
			}
			header := []byte(`{"alg":"RS256","kid":"test"}`)
			if name == "unsigned" {
				header = []byte(`{"alg":"none"}`)
			}
			data, _ := json.Marshal(claims)
			input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(data)
			digest := sha256.Sum256([]byte(input))
			sig, err := rsa.SignPKCS1v15(rand.Reader, signing, crypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			raw := input + "." + base64.RawURLEncoding.EncodeToString(sig)
			if name == "malformed" {
				raw = "bad"
			}
			p, err := v.Verify(context.Background(), raw)
			if name == "valid" {
				if err != nil || p.ProviderID != "provider-a" || p.Internal {
					t.Fatal(p, err)
				}
			} else if name == "internal" {
				if err != nil || !p.Internal || p.ProviderID != "" {
					t.Fatal(p, err)
				}
			} else if err == nil {
				t.Fatal("accepted", name)
			}
		})
	}
}
