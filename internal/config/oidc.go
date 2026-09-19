package config

import (
	"encoding/json"
	"fmt"
	"net/url"
)

type OIDCConfig struct {
	Issuer, Audience, InternalClient string
	ProviderClients                  map[string]string
}

func loadOIDC() (OIDCConfig, error) {
	c := OIDCConfig{Issuer: getEnv("OIDC_ISSUER", "http://localhost:8081/realms/wager"), Audience: getEnv("OIDC_AUDIENCE", "wager-api"), InternalClient: getEnv("OIDC_INTERNAL_CLIENT", "wallet-internal")}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || c.Audience == "" || c.InternalClient == "" {
		return c, fmt.Errorf("invalid OIDC configuration")
	}
	if err = json.Unmarshal([]byte(getEnv("OIDC_PROVIDER_CLIENTS", `{"provider-a":"provider-a","provider-b":"provider-b"}`)), &c.ProviderClients); err != nil || len(c.ProviderClients) == 0 {
		return c, fmt.Errorf("invalid OIDC_PROVIDER_CLIENTS")
	}
	for client, provider := range c.ProviderClients {
		if client == "" || provider == "" || client == c.InternalClient {
			return c, fmt.Errorf("invalid OIDC client mapping")
		}
	}
	return c, nil
}
