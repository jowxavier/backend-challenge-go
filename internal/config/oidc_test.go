package config

import "testing"

func TestOIDCConfiguration(t *testing.T) {
	c, err := loadOIDC()
	if err != nil || c.Issuer == "" || c.Audience == "" || len(c.ProviderClients) != 2 {
		t.Fatal(c, err)
	}
	for _, tc := range []struct{ k, v string }{{"OIDC_ISSUER", ""}, {"OIDC_ISSUER", "not-url"}, {"OIDC_AUDIENCE", ""}, {"OIDC_INTERNAL_CLIENT", ""}, {"OIDC_PROVIDER_CLIENTS", "{}"}, {"OIDC_PROVIDER_CLIENTS", `{"wallet-internal":"provider"}`}} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if _, err := loadOIDC(); err == nil {
				t.Fatal("invalid auth configuration accepted")
			}
		})
	}
}
