package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

// oidcScopes is what every OpenID Connect provider is asked for.
var oidcScopes = []string{"openid", "email", "profile"}

// Google's endpoints are fixed (and published in its discovery document at
// https://accounts.google.com/.well-known/openid-configuration), so they are
// hard-coded instead of fetched at startup.
var googleProvider = newOIDCProvider("google", oauth2.Endpoint{
	AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL:  "https://oauth2.googleapis.com/token",
	AuthStyle: oauth2.AuthStyleInParams,
}, "https://openidconnect.googleapis.com/v1/userinfo")

// newOIDCProvider builds a provider that reads the standard OpenID Connect
// userinfo claims. Any OIDC-compliant identity provider fits.
func newOIDCProvider(name string, endpoint oauth2.Endpoint, userinfoURL string) oauthProvider {
	return oauthProvider{
		name:   name,
		scopes: oidcScopes,
		auth:   endpoint,
		fetch: func(ctx context.Context, client *http.Client) (oauthProfile, error) {
			return fetchOIDCUserinfo(ctx, client, name, userinfoURL)
		},
	}
}

// fetchOIDCUserinfo reads the userinfo endpoint. Only an address the provider
// marks email_verified is accepted, so it is safe to link to a local account.
func fetchOIDCUserinfo(ctx context.Context, client *http.Client, name, userinfoURL string) (oauthProfile, error) {
	var info struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     any    `json:"email_verified"` // bool; some providers send "true"
		PreferredUsername string `json:"preferred_username"`
		GivenName         string `json:"given_name"`
		FamilyName        string `json:"family_name"`
		Name              string `json:"name"`
		Picture           string `json:"picture"`
	}
	if err := getJSON(ctx, client, userinfoURL, &info); err != nil {
		return oauthProfile{}, err
	}
	if info.Sub == "" {
		return oauthProfile{}, fmt.Errorf("%s: missing sub claim", name)
	}
	verified := info.EmailVerified == true || info.EmailVerified == "true"
	email := strings.ToLower(strings.TrimSpace(info.Email))
	if !verified || email == "" {
		return oauthProfile{}, errNoVerifiedEmail
	}
	p := oauthProfile{
		ID:        info.Sub,
		Email:     email,
		FirstName: strings.TrimSpace(info.GivenName),
		LastName:  strings.TrimSpace(info.FamilyName),
		AvatarURL: info.Picture,
	}
	if p.FirstName == "" && p.LastName == "" {
		first, last, _ := strings.Cut(strings.TrimSpace(info.Name), " ")
		p.FirstName, p.LastName = strings.TrimSpace(first), strings.TrimSpace(last)
	}
	// Username suggestion: preferred_username when the provider has one,
	// otherwise the email local part (suggestUsername sanitizes either).
	p.Login = info.PreferredUsername
	if suggestUsername(p.Login) == "" {
		p.Login, _, _ = strings.Cut(email, "@")
	}
	return p, nil
}

// discoverOIDC fetches issuer/.well-known/openid-configuration and builds a
// provider from it. Used for the env-configured generic OIDC provider.
func discoverOIDC(ctx context.Context, client *http.Client, name, issuer string) (oauthProvider, error) {
	issuer = strings.TrimRight(issuer, "/")
	if u, err := url.Parse(issuer); err != nil || u.Scheme != "https" || u.Host == "" {
		return oauthProvider{}, errors.New("issuer must be an https URL")
	}
	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := getJSON(ctx, client, issuer+"/.well-known/openid-configuration", &doc); err != nil {
		return oauthProvider{}, err
	}
	if strings.TrimRight(doc.Issuer, "/") != issuer {
		return oauthProvider{}, fmt.Errorf("discovery issuer %q does not match %q", doc.Issuer, issuer)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.UserinfoEndpoint == "" {
		return oauthProvider{}, errors.New("discovery document lacks authorization/token/userinfo endpoint")
	}
	return newOIDCProvider(name, oauth2.Endpoint{
		AuthURL:  doc.AuthorizationEndpoint,
		TokenURL: doc.TokenEndpoint,
		// AuthStyle left at autodetect: providers differ (header vs params).
	}, doc.UserinfoEndpoint), nil
}
