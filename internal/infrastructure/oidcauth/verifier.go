package oidcauth

import (
	"context"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jowxavier/backend-challenge-go/internal/application/access"
	"github.com/jowxavier/backend-challenge-go/internal/config"
	"go.uber.org/fx"
)

type Verifier struct {
	verifier *oidc.IDTokenVerifier
	cfg      config.OIDCConfig
	client   *http.Client
}

func New(ctx context.Context, c config.OIDCConfig) (*Verifier, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	ctx = oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return nil, err
	}
	return &Verifier{provider.Verifier(&oidc.Config{ClientID: c.Audience, SupportedSigningAlgs: []string{"RS256"}}), c, client}, nil
}
func NewLifecycle(lc fx.Lifecycle, cfg *config.Config) *Verifier {
	v := &Verifier{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		ready, err := New(ctx, cfg.OIDC)
		if err != nil {
			return err
		}
		*v = *ready
		return nil
	}})
	return v
}
func (v *Verifier) Verify(ctx context.Context, raw string) (access.Principal, error) {
	if v.verifier == nil {
		return access.Principal{}, access.ErrUnauthenticated
	}
	token, err := v.verifier.Verify(oidc.ClientContext(ctx, v.client), raw)
	if err != nil {
		return access.Principal{}, access.ErrUnauthenticated
	}
	var claims struct {
		Client    string `json:"azp"`
		NotBefore int64  `json:"nbf"`
	}
	if err = token.Claims(&claims); err != nil || claims.NotBefore > time.Now().Unix() {
		return access.Principal{}, access.ErrUnauthenticated
	}
	if claims.Client == v.cfg.InternalClient {
		return access.Principal{Internal: true}, nil
	}
	provider, ok := v.cfg.ProviderClients[claims.Client]
	if !ok {
		return access.Principal{}, access.ErrUnauthenticated
	}
	return access.Principal{ProviderID: provider}, nil
}
