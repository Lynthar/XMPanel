package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/config"
)

func newTestManager(accessTTL, refreshTTL time.Duration) *JWTManager {
	return NewJWTManager(config.JWTConfig{
		Secret:          "test-secret-key-at-least-32-chars-long-for-hs256",
		AccessTokenTTL:  accessTTL,
		RefreshTokenTTL: refreshTTL,
		Issuer:          "xmpanel-test",
	})
}

func TestJWTManager_GenerateAndValidateAccess(t *testing.T) {
	m := newTestManager(15*time.Minute, time.Hour)
	pair, err := m.GenerateTokenPair(42, "alice", "admin", "sess-abc", "device-1")
	if err != nil {
		t.Fatalf("GenerateTokenPair: %v", err)
	}
	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("empty token in pair")
	}
	if pair.AccessToken == pair.RefreshToken {
		t.Error("access and refresh tokens are identical")
	}

	claims, err := m.ValidateToken(pair.AccessToken, TokenTypeAccess)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("UserID = %d, want 42", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q", claims.Username)
	}
	if claims.Role != "admin" {
		t.Errorf("Role = %q", claims.Role)
	}
	if claims.SessionID != "sess-abc" {
		t.Errorf("SessionID = %q", claims.SessionID)
	}
	if claims.DeviceID != "device-1" {
		t.Errorf("DeviceID = %q", claims.DeviceID)
	}
}

func TestJWTManager_ValidateRefresh(t *testing.T) {
	m := newTestManager(15*time.Minute, time.Hour)
	pair, _ := m.GenerateTokenPair(1, "u", "viewer", "s", "")
	if _, err := m.ValidateToken(pair.RefreshToken, TokenTypeRefresh); err != nil {
		t.Errorf("Validate refresh: %v", err)
	}
}

func TestJWTManager_RejectAccessAsRefresh(t *testing.T) {
	m := newTestManager(15*time.Minute, time.Hour)
	pair, _ := m.GenerateTokenPair(1, "u", "viewer", "s", "")
	// Validating an access token under TokenTypeRefresh must fail.
	if _, err := m.ValidateToken(pair.AccessToken, TokenTypeRefresh); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
	// And vice versa.
	if _, err := m.ValidateToken(pair.RefreshToken, TokenTypeAccess); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expected ErrInvalidToken, got %v", err)
	}
}

func TestJWTManager_ExpiredToken(t *testing.T) {
	// Issue a token that expires almost immediately, then sleep past it.
	m := newTestManager(50*time.Millisecond, time.Hour)
	pair, _ := m.GenerateTokenPair(1, "u", "viewer", "s", "")
	time.Sleep(100 * time.Millisecond)
	_, err := m.ValidateToken(pair.AccessToken, TokenTypeAccess)
	if !errors.Is(err, ErrExpiredToken) {
		t.Errorf("expected ErrExpiredToken, got %v", err)
	}
}

func TestJWTManager_RejectsTamperedSignature(t *testing.T) {
	m := newTestManager(time.Hour, time.Hour)
	pair, _ := m.GenerateTokenPair(1, "u", "viewer", "s", "")
	// Flip last char of the signature segment.
	parts := strings.Split(pair.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT format")
	}
	sig := parts[2]
	last := sig[len(sig)-1]
	if last == 'A' {
		last = 'B'
	} else {
		last = 'A'
	}
	parts[2] = sig[:len(sig)-1] + string(last)
	tampered := strings.Join(parts, ".")
	if _, err := m.ValidateToken(tampered, TokenTypeAccess); err == nil {
		t.Error("tampered token validated successfully")
	}
}

func TestJWTManager_RejectsWrongIssuer(t *testing.T) {
	a := newTestManager(time.Hour, time.Hour)
	b := NewJWTManager(config.JWTConfig{
		Secret:          "test-secret-key-at-least-32-chars-long-for-hs256",
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: time.Hour,
		Issuer:          "different-issuer",
	})
	pair, _ := a.GenerateTokenPair(1, "u", "viewer", "s", "")
	if _, err := b.ValidateToken(pair.AccessToken, TokenTypeAccess); err == nil {
		t.Error("token from different issuer validated successfully")
	}
}

func TestJWTManager_RefreshAccessToken(t *testing.T) {
	m := newTestManager(time.Hour, time.Hour)
	pair, _ := m.GenerateTokenPair(7, "bob", "operator", "sid", "did")

	newPair, err := m.RefreshAccessToken(pair.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshAccessToken: %v", err)
	}
	claims, err := m.ValidateToken(newPair.AccessToken, TokenTypeAccess)
	if err != nil {
		t.Fatalf("validate refreshed access: %v", err)
	}
	if claims.UserID != 7 || claims.SessionID != "sid" {
		t.Errorf("refreshed claims lost identity: %+v", claims)
	}
}
