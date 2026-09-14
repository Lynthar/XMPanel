package password

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/xmpanel/xmpanel/internal/config"
)

var (
	ErrPasswordTooShort  = errors.New("password is too short")
	ErrPasswordNoUpper   = errors.New("password must contain at least one uppercase letter")
	ErrPasswordNoLower   = errors.New("password must contain at least one lowercase letter")
	ErrPasswordNoNumber  = errors.New("password must contain at least one number")
	ErrPasswordNoSpecial = errors.New("password must contain at least one special character")
)

// Validator validates passwords against configured policy
type Validator struct {
	minLength      int
	requireUpper   bool
	requireLower   bool
	requireNumber  bool
	requireSpecial bool
}

// NewValidator creates a new password validator
func NewValidator(cfg config.PasswordConfig) *Validator {
	return &Validator{
		minLength:      cfg.MinLength,
		requireUpper:   cfg.RequireUpper,
		requireLower:   cfg.RequireLower,
		requireNumber:  cfg.RequireNumber,
		requireSpecial: cfg.RequireSpecial,
	}
}

// Validate checks if a password meets the policy requirements
// Returns nil if valid, or an error describing the first failed requirement
func (v *Validator) Validate(password string) error {
	if len(password) < v.minLength {
		return ErrPasswordTooShort
	}

	if v.requireUpper && !hasUppercase(password) {
		return ErrPasswordNoUpper
	}

	if v.requireLower && !hasLowercase(password) {
		return ErrPasswordNoLower
	}

	if v.requireNumber && !hasNumber(password) {
		return ErrPasswordNoNumber
	}

	if v.requireSpecial && !hasSpecial(password) {
		return ErrPasswordNoSpecial
	}

	return nil
}

// ValidateAll checks all requirements and returns all errors
func (v *Validator) ValidateAll(password string) []error {
	var errs []error

	if len(password) < v.minLength {
		errs = append(errs, ErrPasswordTooShort)
	}

	if v.requireUpper && !hasUppercase(password) {
		errs = append(errs, ErrPasswordNoUpper)
	}

	if v.requireLower && !hasLowercase(password) {
		errs = append(errs, ErrPasswordNoLower)
	}

	if v.requireNumber && !hasNumber(password) {
		errs = append(errs, ErrPasswordNoNumber)
	}

	if v.requireSpecial && !hasSpecial(password) {
		errs = append(errs, ErrPasswordNoSpecial)
	}

	return errs
}

// GetRequirements returns a human-readable description of password requirements
func (v *Validator) GetRequirements() string {
	var reqs []string
	reqs = append(reqs, "at least "+strconv.Itoa(v.minLength)+" characters")

	if v.requireUpper {
		reqs = append(reqs, "one uppercase letter")
	}
	if v.requireLower {
		reqs = append(reqs, "one lowercase letter")
	}
	if v.requireNumber {
		reqs = append(reqs, "one number")
	}
	if v.requireSpecial {
		reqs = append(reqs, "one special character")
	}

	return "Password must contain " + strings.Join(reqs, ", ")
}

func hasUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func hasLowercase(s string) bool {
	for _, r := range s {
		if unicode.IsLower(r) {
			return true
		}
	}
	return false
}

func hasNumber(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func hasSpecial(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
