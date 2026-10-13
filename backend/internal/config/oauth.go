package config

import (
	"os"
	"strings"
)

// OAuthClient is one provider's app credentials. A provider is disabled
// unless both values are set.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
}

// Enabled reports whether the provider is configured.
func (c OAuthClient) Enabled() bool { return c.ClientID != "" && c.ClientSecret != "" }

// OAuthConfig holds the OAuth login settings.
type OAuthConfig struct {
	// PublicURL is the external origin (scheme://host[:port], no path) the
	// browser uses; callback URLs are PublicURL + BASE_PATH + /api/auth/<provider>/callback.
	PublicURL string
	FortyTwo  OAuthClient
	GitHub    OAuthClient
	Google    OAuthClient
	// OIDC is an optional extra OpenID Connect provider configured entirely
	// from env (endpoints come from issuer discovery).
	OIDC OIDCProvider
}

// OIDCProvider is a generic OpenID Connect login provider. Disabled unless
// name, issuer and client credentials are all set.
type OIDCProvider struct {
	Name   string // URL segment and oauth_identities.provider value, e.g. "drevia"
	Issuer string // https issuer; <issuer>/.well-known/openid-configuration is fetched
	OAuthClient
}

// Enabled reports whether the provider is fully configured.
func (p OIDCProvider) Enabled() bool {
	return p.Name != "" && p.Issuer != "" && p.OAuthClient.Enabled()
}

func loadOAuth() OAuthConfig {
	// OAUTH_REDIRECT_BASE_URL is the older name for PUBLIC_URL.
	public := envStr("PUBLIC_URL", envStr("OAUTH_REDIRECT_BASE_URL", "http://localhost:4200"))
	return OAuthConfig{
		PublicURL: strings.TrimRight(public, "/"),
		FortyTwo: OAuthClient{
			ClientID:     os.Getenv("FORTYTWO_CLIENT_ID"),
			ClientSecret: os.Getenv("FORTYTWO_CLIENT_SECRET"),
		},
		GitHub: OAuthClient{
			ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
			ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		},
		Google: OAuthClient{
			ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
			ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		},
		OIDC: OIDCProvider{
			Name:   strings.ToLower(strings.TrimSpace(os.Getenv("OIDC_PROVIDER_NAME"))),
			Issuer: strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
			OAuthClient: OAuthClient{
				ClientID:     os.Getenv("OIDC_CLIENT_ID"),
				ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
			},
		},
	}
}
