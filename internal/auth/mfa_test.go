package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/security/crypto"
)

func TestTOTPManager_GenerateSecret(t *testing.T) {
	m := NewTOTPManager("XMPanel")
	s, err := m.GenerateSecret("alice")
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if s.Secret == "" {
		t.Error("empty secret")
	}
	if !strings.Contains(s.URI, "otpauth://totp/XMPanel:alice") {
		t.Errorf("URI missing issuer/user: %q", s.URI)
	}
	// Secret should be base32 (no padding) and decode cleanly.
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s.Secret); err != nil {
		t.Errorf("secret not valid base32: %v", err)
	}
}

func TestTOTPManager_ValidateCode_CurrentWindow(t *testing.T) {
	m := NewTOTPManager("XMPanel")
	s, _ := m.GenerateSecret("alice")
	secretBytes, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s.Secret)

	now := time.Now().Unix()
	code := generateTOTP(secretBytes, now/totpPeriod)

	ok, err := m.ValidateCode(s.Secret, code)
	if err != nil || !ok {
		t.Errorf("current-window code rejected: ok=%v err=%v", ok, err)
	}
}

func TestTOTPManager_ValidateCode_AcceptsAdjacentWindow(t *testing.T) {
	// Codes from the previous window must still validate within ±totpSkew.
	m := NewTOTPManager("XMPanel")
	s, _ := m.GenerateSecret("alice")
	secretBytes, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s.Secret)

	prev := generateTOTP(secretBytes, (time.Now().Unix()/totpPeriod)-1)
	ok, err := m.ValidateCode(s.Secret, prev)
	if err != nil || !ok {
		t.Errorf("previous-window code rejected: ok=%v err=%v", ok, err)
	}
}

func TestTOTPManager_RejectsCodeOutsideSkew(t *testing.T) {
	m := NewTOTPManager("XMPanel")
	s, _ := m.GenerateSecret("alice")
	secretBytes, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s.Secret)

	old := generateTOTP(secretBytes, (time.Now().Unix()/totpPeriod)-10)
	ok, _ := m.ValidateCode(s.Secret, old)
	if ok {
		t.Error("code from 10 windows ago accepted")
	}
}

func TestTOTPManager_RejectsWrongLength(t *testing.T) {
	m := NewTOTPManager("XMPanel")
	s, _ := m.GenerateSecret("alice")
	if ok, _ := m.ValidateCode(s.Secret, "12345"); ok {
		t.Error("5-digit code accepted")
	}
	if ok, _ := m.ValidateCode(s.Secret, "1234567"); ok {
		t.Error("7-digit code accepted")
	}
}

func TestRecoveryCodeManager_GenerateAndVerify(t *testing.T) {
	m := NewRecoveryCodeManager()
	codes, err := m.GenerateCodes()
	if err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Errorf("len(codes)=%d, want 10", len(codes))
	}
	for _, c := range codes {
		if !strings.Contains(c, "-") {
			t.Errorf("code %q missing dash separator", c)
		}
	}

	hasher := crypto.NewArgon2Hasher(1, 8*1024, 1)
	hashed, err := m.HashCodes(codes, hasher)
	if err != nil {
		t.Fatalf("HashCodes: %v", err)
	}

	// Each plain code should verify against its hash.
	for i, code := range codes {
		idx, ok, err := m.VerifyCode(code, hashed, hasher)
		if err != nil || !ok {
			t.Errorf("code %d: ok=%v err=%v", i, ok, err)
		}
		if idx != i {
			t.Errorf("code %d: VerifyCode returned idx=%d", i, idx)
		}
	}

	// Wrong code returns -1, false.
	idx, ok, err := m.VerifyCode("WRONG-CODE", hashed, hasher)
	if err != nil {
		t.Errorf("VerifyCode(wrong) returned error: %v", err)
	}
	if ok || idx != -1 {
		t.Errorf("wrong code accepted: idx=%d ok=%v", idx, ok)
	}
}

func TestRecoveryCodeManager_SkipsConsumedCodes(t *testing.T) {
	// VerifyCode treats empty hashed slots as already-used and skips them.
	m := NewRecoveryCodeManager()
	codes, _ := m.GenerateCodes()
	hasher := crypto.NewArgon2Hasher(1, 8*1024, 1)
	hashed, _ := m.HashCodes(codes, hasher)

	hashed[3] = "" // Mark as consumed.
	if idx, ok, _ := m.VerifyCode(codes[3], hashed, hasher); ok {
		t.Errorf("consumed code accepted at idx=%d", idx)
	}
}
