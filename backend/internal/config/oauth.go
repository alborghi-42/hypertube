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
	}
}
