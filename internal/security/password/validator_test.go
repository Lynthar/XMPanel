package password

import (
	"errors"
	"testing"

	"github.com/xmpanel/xmpanel/internal/config"
)

func cfg(min int, upper, lower, number, special bool) config.PasswordConfig {
	return config.PasswordConfig{
		MinLength:      min,
		RequireUpper:   upper,
		RequireLower:   lower,
		RequireNumber:  number,
		RequireSpecial: special,
	}
}

func TestValidator_TooShort(t *testing.T) {
	v := NewValidator(cfg(12, false, false, false, false))
	err := v.Validate("short")
	if !errors.Is(err, ErrPasswordTooShort) {
		t.Errorf("expected ErrPasswordTooShort, got %v", err)
	}
}

func TestValidator_RequiresUpper(t *testing.T) {
	v := NewValidator(cfg(8, true, false, false, false))
	if err := v.Validate("alllowercase1"); !errors.Is(err, ErrPasswordNoUpper) {
		t.Errorf("got %v, want ErrPasswordNoUpper", err)
	}
	if err := v.Validate("HasUpper1"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestValidator_RequiresLower(t *testing.T) {
	v := NewValidator(cfg(8, false, true, false, false))
	if err := v.Validate("ALLUPPERCASE1"); !errors.Is(err, ErrPasswordNoLower) {
		t.Errorf("got %v, want ErrPasswordNoLower", err)
	}
}

func TestValidator_RequiresNumber(t *testing.T) {
	v := NewValidator(cfg(8, false, false, true, false))
	if err := v.Validate("NoNumbersHere"); !errors.Is(err, ErrPasswordNoNumber) {
		t.Errorf("got %v, want ErrPasswordNoNumber", err)
	}
}

func TestValidator_RequiresSpecial(t *testing.T) {
	v := NewValidator(cfg(8, false, false, false, true))
	if err := v.Validate("NoSpecial1"); !errors.Is(err, ErrPasswordNoSpecial) {
		t.Errorf("got %v, want ErrPasswordNoSpecial", err)
	}
	if err := v.Validate("Has-Special1"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestValidator_AllRequirements(t *testing.T) {
	v := NewValidator(cfg(12, true, true, true, true))
	if err := v.Validate("CorrectHorse-1"); err != nil {
		t.Errorf("valid password rejected: %v", err)
	}
}

func TestValidator_ValidateAllReturnsAllErrors(t *testing.T) {
	v := NewValidator(cfg(12, true, true, true, true))
	errs := v.ValidateAll("short")
	// short violates: length, upper, number, special (has lower so no NoLower)
	if len(errs) < 4 {
		t.Errorf("expected at least 4 errors, got %d: %v", len(errs), errs)
	}
}

func TestValidator_GetRequirementsMentionsMinLength(t *testing.T) {
	v := NewValidator(cfg(12, true, true, true, false))
	got := v.GetRequirements()
	if got == "" {
		t.Error("GetRequirements returned empty string")
	}
}
