package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

// AccountUpdate is a partial account change made through the public REST
// API (PATCH /api/v1/users/:id): nil fields keep their value.
type AccountUpdate struct {
	Username  *string
	Email     *string
	Password  *string
	AvatarURL *string // absolute http(s) URL, or "" to remove the picture
}

// ErrAccountConflict is returned when the new username or email is taken;
// the field errors say which.
var ErrAccountConflict = errors.New("account conflict")

const avatarURLMax = 500

// validateAvatarURL accepts our own uploaded avatars and absolute http(s)
// URLs only, so a javascript: or data: URL can never end up in an <img>.
func validateAvatarURL(s string) string {
	if s == "" || strings.HasPrefix(s, avatarURLPrefix) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || len(s) > avatarURLMax || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "Profile picture must be an http(s) URL"
	}
	return ""
}

// UpdateAccount validates and applies upd to current with the same rules as
// the profile page and registration. It returns the field errors (non-nil
// map, possibly with ErrAccountConflict) when the input is rejected. A
// password change also revokes the user's web sessions, like a reset.
func (h *ProfileHandler) UpdateAccount(ctx context.Context, current *repository.User, upd AccountUpdate) (*repository.User, map[string]string, error) {
	merged := registerRequest{
		Email:     pick(upd.Email, current.Email),
		Username:  pick(upd.Username, current.Username),
		FirstName: current.FirstName,
		LastName:  current.LastName,
	}
	merged.normalize()

	errs := fieldErrors{}
	if msg := validateUsername(merged.Username); msg != "" {
		errs["username"] = msg
	}
	if msg := validateEmail(merged.Email); msg != "" {
		errs["email"] = msg
	}
	if upd.Password != nil {
		if msg := ValidatePassword(*upd.Password); msg != "" {
			errs["password"] = msg
		}
	}
	var avatar string
	if upd.AvatarURL != nil {
		avatar = strings.TrimSpace(*upd.AvatarURL)
		if msg := validateAvatarURL(avatar); msg != "" {
			errs["avatar_url"] = msg
		}
	}
	if len(errs) > 0 {
		return nil, errs, nil
	}

	var hash string
	if upd.Password != nil {
		var err error
		if hash, err = HashPassword(*upd.Password); err != nil {
			return nil, nil, err
		}
	}

	user, err := h.q.UpdateUserProfile(ctx, repository.UpdateUserProfileParams{
		ID:        current.ID,
		Username:  merged.Username,
		Email:     merged.Email,
		FirstName: current.FirstName,
		LastName:  current.LastName,
		Language:  current.Language,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if strings.Contains(pgErr.ConstraintName, "email") {
				return nil, fieldErrors{"email": "Email is already registered"}, ErrAccountConflict
			}
			return nil, fieldErrors{"username": "Username is already taken"}, ErrAccountConflict
		}
		return nil, nil, err
	}

	if upd.AvatarURL != nil && pick(current.AvatarUrl, "") != avatar {
		var next *string
		if avatar != "" {
			next = &avatar
		}
		if err := h.q.UpdateUserAvatar(ctx, repository.UpdateUserAvatarParams{ID: current.ID, AvatarUrl: next}); err != nil {
			return nil, nil, err
		}
		h.avatars.remove(current.AvatarUrl)
		user.AvatarUrl = next
	}

	if upd.Password != nil {
		if err := h.q.UpdateUserPassword(ctx, repository.UpdateUserPasswordParams{ID: current.ID, PasswordHash: &hash}); err != nil {
			return nil, nil, err
		}
		if err := h.q.DeleteUserSessions(ctx, current.ID); err != nil {
			h.log.Error("revoke sessions after API password change failed", "error", err)
		}
	}
	return &user, nil, nil
}
