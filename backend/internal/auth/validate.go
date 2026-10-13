package auth

import (
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validation limits; mirrored by the Angular forms (frontend/src/app/auth/validation.ts).
const (
	usernameMin = 3
	usernameMax = 30
	nameMax     = 50
	emailMax    = 254
	passwordMin = 8
	passwordMax = 128
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// fieldErrors maps a JSON field name to a human-readable message.
type fieldErrors map[string]string

type registerRequest struct {
	Email     string `json:"email"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Password  string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// normalize trims the free-text fields and lowercases the email in place.
func (r *registerRequest) normalize() {
	r.Email = strings.ToLower(strings.TrimSpace(r.Email))
	r.Username = strings.TrimSpace(r.Username)
	r.FirstName = strings.TrimSpace(r.FirstName)
	r.LastName = strings.TrimSpace(r.LastName)
}

func (r *registerRequest) validate() fieldErrors {
	errs := fieldErrors{}
	if msg := validateUsername(r.Username); msg != "" {
		errs["username"] = msg
	}
	if msg := validateEmail(r.Email); msg != "" {
		errs["email"] = msg
	}
	if msg := validateName(r.FirstName); msg != "" {
		errs["first_name"] = msg
	}
	if msg := validateName(r.LastName); msg != "" {
		errs["last_name"] = msg
	}
	if msg := ValidatePassword(r.Password); msg != "" {
		errs["password"] = msg
	}
	return errs
}

func validateUsername(s string) string {
	n := utf8.RuneCountInString(s)
	switch {
	case n == 0:
		return "Username is required"
	case n < usernameMin || n > usernameMax:
		return "Username must be 3 to 30 characters"
	case !usernamePattern.MatchString(s):
		return "Username may only contain letters, digits, '.', '_' and '-'"
	}
	return ""
}

func validateEmail(s string) string {
	if s == "" {
		return "Email is required"
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || len(s) > emailMax || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
		return "Email address is invalid"
	}
	return ""
}

func validateName(s string) string {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return "This field is required"
	}
	if n > nameMax {
		return "Must be at most 50 characters"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "Contains invalid characters"
		}
	}
	return ""
}

// ValidatePassword is exported for the password-reset flow.
func ValidatePassword(s string) string {
	n := utf8.RuneCountInString(s)
	if n < passwordMin || n > passwordMax {
		return "Password must be 8 to 128 characters"
	}
	var letter, digit bool
	for _, r := range s {
		letter = letter || unicode.IsLetter(r)
		digit = digit || unicode.IsDigit(r)
	}
	if !letter || !digit {
		return "Password must contain at least one letter and one digit"
	}
	return ""
}
