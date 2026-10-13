package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/oauth2"

	"github.com/alborghi-42/hypertube/backend/internal/config"
	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const (
	// oauthFlowCookie binds an in-progress authorization (state + PKCE
	// verifier, kept server-side) to the browser that started it.
	oauthFlowCookie = "ht_oauth_flow"
	// oauthSignupCookie binds a pending first-login signup to the browser.
	oauthSignupCookie = "ht_oauth_signup"

	oauthFlowTTL   = 10 * time.Minute
	oauthSignupTTL = 30 * time.Minute

	// Frontend routes (relative to BASE_PATH) the callback redirects to.
	oauthLoginPage    = "/login"
	oauthCompletePage = "/oauth/complete"
)

// OAuthDB is a pool: queries plus transactions.
type OAuthDB interface {
	repository.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// oauthFlow is the server-side half of an authorization request.
type oauthFlow struct {
	provider  string
	state     string
	verifier  string
	returnURL string
}

// pendingSignup is a verified provider identity with no account yet; the
// user picks the missing profile fields on the complete-profile page.
type pendingSignup struct {
	provider  string
	profile   oauthProfile
	username  string // suggestion
	returnURL string
}

// ttlStore is a tiny in-memory, per-process store with expiry (single
// replica, like the login limiter). Entries are one-shot or short-lived.
type ttlStore[T any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ttlEntry[T]
}

type ttlEntry[T any] struct {
	value   T
	expires time.Time
}

func newTTLStore[T any](ttl time.Duration) *ttlStore[T] {
	return &ttlStore[T]{ttl: ttl, entries: map[string]ttlEntry[T]{}}
}

func (s *ttlStore[T]) put(key string, v T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, k)
		}
	}
	s.entries[key] = ttlEntry[T]{value: v, expires: now.Add(s.ttl)}
}

func (s *ttlStore[T]) get(key string, remove bool) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if ok && remove {
		delete(s.entries, key)
	}
	if !ok || time.Now().After(e.expires) {
		var zero T
		return zero, false
	}
	return e.value, true
}

type oauthClient struct {
	provider oauthProvider
	config   *oauth2.Config
}

// OAuth serves the 42 intra / GitHub / Google (+ optional generic OIDC) login endpoints.
type OAuth struct {
	db       OAuthDB
	q        *repository.Queries
	sessions *Sessions
	cookies  CookieOptions
	basePath string
	clients  map[string]*oauthClient
	routes   []string // provider names with start/callback routes
	flows    *ttlStore[oauthFlow]
	signups  *ttlStore[pendingSignup]
	http     *http.Client
	log      *slog.Logger
}

// NewOAuth enables every provider that has credentials in cfg. Callback URLs
// are cfg.PublicURL + basePath + /api/auth/<provider>/callback.
func NewOAuth(db OAuthDB, sessions *Sessions, cookies CookieOptions, basePath string, cfg config.OAuthConfig, log *slog.Logger) *OAuth {
	o := &OAuth{
		db:       db,
		q:        repository.New(db),
		sessions: sessions,
		cookies:  cookies,
		basePath: basePath,
		clients:  map[string]*oauthClient{},
		flows:    newTTLStore[oauthFlow](oauthFlowTTL),
		signups:  newTTLStore[pendingSignup](oauthSignupTTL),
		http:     &http.Client{Timeout: 10 * time.Second},
		log:      log,
	}
	type enabled struct {
		provider oauthProvider
		creds    config.OAuthClient
	}
	providers := []enabled{{fortyTwoProvider, cfg.FortyTwo}, {gitHubProvider, cfg.GitHub}, {googleProvider, cfg.Google}}
	for _, p := range providers {
		o.routes = append(o.routes, p.provider.name)
	}
	if name := cfg.OIDC.Name; name != "" {
		if err := o.validOIDCName(name); err != nil {
			log.Error("oidc provider ignored", "provider", name, "error", err)
		} else {
			// Routes exist even when discovery fails, so the button reports
			// "unavailable" instead of a 404.
			o.routes = append(o.routes, name)
			if cfg.OIDC.Enabled() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				p, err := discoverOIDC(ctx, o.http, name, cfg.OIDC.Issuer)
				cancel()
				if err != nil {
					log.Error("oidc discovery failed, provider disabled", "provider", name, "issuer", cfg.OIDC.Issuer, "error", err)
				} else {
					providers = append(providers, enabled{p, cfg.OIDC.OAuthClient})
				}
			}
		}
	}
	for _, p := range providers {
		if !p.creds.Enabled() {
			log.Info("oauth provider disabled (no credentials)", "provider", p.provider.name)
			continue
		}
		o.clients[p.provider.name] = &oauthClient{
			provider: p.provider,
			config: &oauth2.Config{
				ClientID:     p.creds.ClientID,
				ClientSecret: p.creds.ClientSecret,
				Endpoint:     p.provider.auth,
				Scopes:       p.provider.scopes,
				RedirectURL:  cfg.PublicURL + basePath + "/api/auth/" + p.provider.name + "/callback",
			},
		}
	}
	return o
}

var oidcNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// validOIDCName rejects names that are not a clean URL segment or that would
// collide with a built-in provider or the /auth/oauth/signup route.
func (o *OAuth) validOIDCName(name string) error {
	if !oidcNamePattern.MatchString(name) {
		return errors.New("OIDC_PROVIDER_NAME must match [a-z0-9-], max 32 chars")
	}
	if name == "oauth" || slices.Contains(o.routes, name) {
		return errors.New("OIDC_PROVIDER_NAME is reserved")
	}
	return nil
}

// RegisterRoutes mounts the public OAuth endpoints on api. Routes exist for
// every provider; disabled ones redirect back to the login page with an error.
func (o *OAuth) RegisterRoutes(api *gin.RouterGroup) {
	for _, name := range o.routes {
		api.GET("/auth/"+name+"/start", o.start(name))
		api.GET("/auth/"+name+"/callback", o.callback(name))
	}
	api.GET("/auth/oauth/signup", o.pending)
	api.POST("/auth/oauth/signup", o.complete)
}

// safeReturnPath mirrors the frontend's safeReturnUrl: only same-app absolute
// paths ("/watch/1"), never "//host", "/\host", schemes or control chars.
func safeReturnPath(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") || len(v) > 2048 {
		return "/"
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "/"
		}
	}
	return v
}

// redirect sends the browser to an app page under BASE_PATH.
func (o *OAuth) redirect(c *gin.Context, path string) {
	c.Redirect(http.StatusFound, o.basePath+path)
}

func (o *OAuth) fail(c *gin.Context, code string) {
	o.redirect(c, oauthLoginPage+"?oauth_error="+url.QueryEscape(code))
}

// GET /api/auth/<provider>/start?returnUrl=/path
func (o *OAuth) start(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		client := o.clients[name]
		if client == nil {
			o.fail(c, "unavailable")
			return
		}
		key, err1 := newToken()
		state, err2 := newToken()
		if err1 != nil || err2 != nil {
			o.fail(c, "failed")
			return
		}
		verifier := oauth2.GenerateVerifier()
		o.flows.put(key, oauthFlow{
			provider:  name,
			state:     state,
			verifier:  verifier,
			returnURL: safeReturnPath(c.Query("returnUrl")),
		})
		o.cookies.set(c, oauthFlowCookie, key, oauthFlowTTL, true)
		c.Redirect(http.StatusFound, client.config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)))
	}
}

// GET /api/auth/<provider>/callback?code=...&state=...
func (o *OAuth) callback(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		client := o.clients[name]
		if client == nil {
			o.fail(c, "unavailable")
			return
		}
		key, _ := c.Cookie(oauthFlowCookie)
		o.cookies.set(c, oauthFlowCookie, "", 0, true)
		flow, ok := o.flows.get(key, true)
		state := c.Query("state")
		if key == "" || !ok || flow.provider != name || state == "" ||
			subtle.ConstantTimeCompare([]byte(state), []byte(flow.state)) != 1 {
			o.fail(c, "state")
			return
		}
		if c.Query("error") != "" || c.Query("code") == "" {
			o.fail(c, "denied")
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, oauth2.HTTPClient, o.http)
		token, err := client.config.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(flow.verifier))
		if err != nil {
			o.log.Warn("oauth code exchange failed", "provider", name, "error", err)
			o.fail(c, "failed")
			return
		}
		profile, err := client.provider.fetch(ctx, client.config.Client(ctx, token))
		if errors.Is(err, errNoVerifiedEmail) || (err == nil && validateEmail(profile.Email) != "") {
			o.fail(c, "email")
			return
		}
		if err != nil {
			o.log.Warn("oauth profile fetch failed", "provider", name, "error", err)
			o.fail(c, "failed")
			return
		}
		o.resolve(c, ctx, name, profile, flow.returnURL)
	}
}

// resolve logs in the linked user, links by verified email, or starts a signup.
func (o *OAuth) resolve(c *gin.Context, ctx context.Context, provider string, p oauthProfile, returnURL string) {
	user, err := o.q.GetOAuthIdentityUser(ctx, repository.GetOAuthIdentityUserParams{Provider: provider, ProviderUserID: p.ID})
	if err == nil {
		o.login(c, user, returnURL)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		o.log.Error("load oauth identity failed", "error", err)
		o.fail(c, "failed")
		return
	}

	// Both sides verified the address (local accounts by registration, the
	// provider by its own check), so link the identity to the existing user.
	user, err = o.q.GetUserByEmail(ctx, p.Email)
	if err == nil {
		if err := o.q.CreateOAuthIdentity(ctx, repository.CreateOAuthIdentityParams{
			Provider: provider, ProviderUserID: p.ID, UserID: user.ID,
		}); err != nil {
			o.log.Error("link oauth identity failed", "error", err)
			o.fail(c, "failed")
			return
		}
		o.login(c, user, returnURL)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		o.log.Error("load user by email failed", "error", err)
		o.fail(c, "failed")
		return
	}

	signup := pendingSignup{provider: provider, profile: p, username: suggestUsername(p.Login), returnURL: returnURL}
	// Everything needed is there and valid: create the account right away.
	if validateUsername(signup.username) == "" && validateName(p.FirstName) == "" && validateName(p.LastName) == "" {
		user, err := o.createUser(ctx, signup, signup.username, p.FirstName, p.LastName)
		if err == nil {
			o.login(c, user, returnURL)
			return
		}
		if !isUniqueViolation(err) {
			o.log.Error("create oauth user failed", "error", err)
			o.fail(c, "failed")
			return
		}
		// Username (or email, raced) taken: let the user choose.
	}
	key, err := newToken()
	if err != nil {
		o.fail(c, "failed")
		return
	}
	o.signups.put(key, signup)
	o.cookies.set(c, oauthSignupCookie, key, oauthSignupTTL, true)
	o.redirect(c, oauthCompletePage)
}

func (o *OAuth) login(c *gin.Context, user repository.User, returnURL string) {
	if err := o.sessions.Start(c, user.ID); err != nil {
		o.log.Error("start session failed", "error", err)
		o.fail(c, "failed")
		return
	}
	o.redirect(c, returnURL)
}

// createUser inserts the user and its identity atomically.
func (o *OAuth) createUser(ctx context.Context, s pendingSignup, username, first, last string) (repository.User, error) {
	tx, err := o.db.Begin(ctx)
	if err != nil {
		return repository.User{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	q := o.q.WithTx(tx)
	var avatar *string
	if s.profile.AvatarURL != "" && strings.HasPrefix(s.profile.AvatarURL, "https://") {
		avatar = &s.profile.AvatarURL
	}
	user, err := q.CreateOAuthUser(ctx, repository.CreateOAuthUserParams{
		Username:  username,
		Email:     s.profile.Email,
		FirstName: first,
		LastName:  last,
		AvatarUrl: avatar,
	})
	if err != nil {
		return repository.User{}, err
	}
	if err := q.CreateOAuthIdentity(ctx, repository.CreateOAuthIdentityParams{
		Provider: s.provider, ProviderUserID: s.profile.ID, UserID: user.ID,
	}); err != nil {
		return repository.User{}, err
	}
	return user, tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// suggestUsername keeps the allowed characters of the provider login.
func suggestUsername(login string) string {
	var b strings.Builder
	for _, r := range login {
		if r < unicode.MaxASCII && usernamePattern.MatchString(string(r)) {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if len(s) > usernameMax {
		s = s[:usernameMax]
	}
	return s
}

func (o *OAuth) pendingSignup(c *gin.Context) (string, pendingSignup, bool) {
	key, _ := c.Cookie(oauthSignupCookie)
	if key == "" {
		return "", pendingSignup{}, false
	}
	s, ok := o.signups.get(key, false)
	return key, s, ok
}

// GET /api/auth/oauth/signup — the prefilled complete-profile form.
func (o *OAuth) pending(c *gin.Context) {
	_, s, ok := o.pendingSignup(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "No pending sign-up, please log in again"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"provider":   s.provider,
		"email":      s.profile.Email,
		"username":   s.username,
		"first_name": s.profile.FirstName,
		"last_name":  s.profile.LastName,
	})
}

type completeSignupRequest struct {
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// POST /api/auth/oauth/signup — creates the account and logs it in.
func (o *OAuth) complete(c *gin.Context) {
	key, s, ok := o.pendingSignup(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "No pending sign-up, please log in again"})
		return
	}
	var req completeSignupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	errs := fieldErrors{}
	if msg := validateUsername(req.Username); msg != "" {
		errs["username"] = msg
	}
	if msg := validateName(req.FirstName); msg != "" {
		errs["first_name"] = msg
	}
	if msg := validateName(req.LastName); msg != "" {
		errs["last_name"] = msg
	}
	if len(errs) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Validation failed", "fields": errs})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	user, err := o.createUser(ctx, s, req.Username, req.FirstName, req.LastName)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if strings.Contains(pgErr.ConstraintName, "username") {
				msg := "Username is already taken"
				c.JSON(http.StatusConflict, gin.H{"error": msg, "fields": fieldErrors{"username": msg}})
				return
			}
			c.JSON(http.StatusConflict, gin.H{"error": "This account already exists, please log in again"})
			return
		}
		o.log.Error("create oauth user failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create account"})
		return
	}
	o.signups.get(key, true)
	o.cookies.set(c, oauthSignupCookie, "", 0, true)
	if err := o.sessions.Start(c, user.ID); err != nil {
		o.log.Error("start session failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Account created but login failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": selfView(&user), "return_url": s.returnURL})
}
