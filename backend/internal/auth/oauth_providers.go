package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// errNoVerifiedEmail means the provider account has no usable verified email,
// so it can neither be linked nor used to create an account.
var errNoVerifiedEmail = errors.New("provider returned no verified email")

// oauthProfile is what we need from a provider account.
type oauthProfile struct {
	ID        string // stable provider user id
	Email     string // verified, lowercased
	Login     string // provider username, used as a suggestion only
	FirstName string
	LastName  string
	AvatarURL string
}

// oauthProvider describes one login provider.
type oauthProvider struct {
	name   string // URL segment and oauth_identities.provider value
	scopes []string
	auth   oauth2.Endpoint
	fetch  func(ctx context.Context, client *http.Client) (oauthProfile, error)
}

var fortyTwoProvider = oauthProvider{
	name:   "42",
	scopes: []string{"public"},
	auth: oauth2.Endpoint{
		AuthURL:   "https://api.intra.42.fr/oauth/authorize",
		TokenURL:  "https://api.intra.42.fr/oauth/token",
		AuthStyle: oauth2.AuthStyleInParams,
	},
	fetch: fetchFortyTwo,
}

var gitHubProvider = oauthProvider{
	name:   "github",
	scopes: []string{"read:user", "user:email"},
	auth: oauth2.Endpoint{
		AuthURL:   "https://github.com/login/oauth/authorize",
		TokenURL:  "https://github.com/login/oauth/access_token",
		AuthStyle: oauth2.AuthStyleInParams,
	},
	fetch: fetchGitHub,
}

const maxProviderBody = 1 << 20

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxProviderBody)).Decode(out)
}

// fetchFortyTwo reads /v2/me. Intra emails are issued/verified by the school.
func fetchFortyTwo(ctx context.Context, client *http.Client) (oauthProfile, error) {
	var me struct {
		ID        int64  `json:"id"`
		Email     string `json:"email"`
		Login     string `json:"login"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Image     struct {
			Link string `json:"link"`
		} `json:"image"`
	}
	if err := getJSON(ctx, client, "https://api.intra.42.fr/v2/me", &me); err != nil {
		return oauthProfile{}, err
	}
	if me.ID == 0 {
		return oauthProfile{}, errors.New("42: missing user id")
	}
	p := oauthProfile{
		ID:        strconv.FormatInt(me.ID, 10),
		Email:     strings.ToLower(strings.TrimSpace(me.Email)),
		Login:     me.Login,
		FirstName: strings.TrimSpace(me.FirstName),
		LastName:  strings.TrimSpace(me.LastName),
		AvatarURL: me.Image.Link,
	}
	if p.Email == "" {
		return oauthProfile{}, errNoVerifiedEmail
	}
	return p, nil
}

// fetchGitHub reads /user and takes the primary verified address from
// /user/emails (the public /user email may be unverified or hidden).
func fetchGitHub(ctx context.Context, client *http.Client) (oauthProfile, error) {
	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user", &user); err != nil {
		return oauthProfile{}, err
	}
	if user.ID == 0 {
		return oauthProfile{}, errors.New("github: missing user id")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return oauthProfile{}, err
	}
	p := oauthProfile{ID: strconv.FormatInt(user.ID, 10), Login: user.Login, AvatarURL: user.AvatarURL}
	for _, e := range emails {
		if e.Primary && e.Verified {
			p.Email = strings.ToLower(strings.TrimSpace(e.Email))
		}
	}
	if p.Email == "" {
		return oauthProfile{}, errNoVerifiedEmail
	}
	first, last, _ := strings.Cut(strings.TrimSpace(user.Name), " ")
	p.FirstName, p.LastName = strings.TrimSpace(first), strings.TrimSpace(last)
	return p, nil
}
