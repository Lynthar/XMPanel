package auth

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/security/crypto"
)

var (
	ErrInvalidTOTPCode = errors.New("invalid TOTP code")
	ErrTOTPNotEnabled  = errors.New("TOTP not enabled for user")
)

const (
	totpDigits   = 6
	totpPeriod   = 30
	totpSkew     = 1 // Allow 1 period before/after
	secretLength = 20
)

// TOTPManager handles TOTP (Time-based One-Time Password) operations
type TOTPManager struct {
	issuer string
}

// NewTOTPManager creates a new TOTP manager
func NewTOTPManager(issuer string) *TOTPManager {
	return &TOTPManager{
		issuer: issuer,
	}
}

// TOTPSecret represents a TOTP secret for a user
type TOTPSecret struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
	QRCode string `json:"qr_code,omitempty"` // Base64 encoded QR code image
}

// GenerateSecret generates a new TOTP secret for a user
func (m *TOTPManager) GenerateSecret(username string) (*TOTPSecret, error) {
	// Generate random secret
	secretBytes, err := crypto.GenerateRandomBytes(secretLength)
	if err != nil {
		return nil, fmt.Errorf("failed to generate secret: %w", err)
	}

	// Encode as base32 (standard TOTP format)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)

	// Generate otpauth URI
	uri := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		m.issuer, username, secret, m.issuer, totpDigits, totpPeriod)

	return &TOTPSecret{
		Secret: secret,
		URI:    uri,
	}, nil
}

// ValidateCode validates a TOTP code against a secret
func (m *TOTPManager) ValidateCode(secret, code string) (bool, error) {
	if len(code) != totpDigits {
		return false, nil
	}

	// Decode secret from base32
	secretBytes, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		return false, fmt.Errorf("failed to decode secret: %w", err)
	}

	// Get current time counter
	now := time.Now().Unix()
	counter := now / totpPeriod

	// Check current and adjacent time periods (to handle clock skew)
	for i := -totpSkew; i <= totpSkew; i++ {
		expectedCode := generateTOTP(secretBytes, counter+int64(i))
		if code == expectedCode {
			return true, nil
		}
	}

	return false, nil
}

// generateTOTP generates a TOTP code for a given counter
func generateTOTP(secret []byte, counter int64) string {
	// Convert counter to big-endian bytes
	counterBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(counterBytes, uint64(counter))

	// Calculate HMAC-SHA1
	h := hmac.New(sha1.New, secret)
	h.Write(counterBytes)
	hash := h.Sum(nil)

	// Dynamic truncation
	offset := hash[len(hash)-1] & 0x0f
	truncatedHash := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff

	// Generate code
	code := truncatedHash % 1000000
	return fmt.Sprintf("%06d", code)
}

// RecoveryCodeManager handles backup/recovery codes
type RecoveryCodeManager struct {
	codeCount  int
	codeLength int
}

// NewRecoveryCodeManager creates a new recovery code manager
func NewRecoveryCodeManager() *RecoveryCodeManager {
	return &RecoveryCodeManager{
		codeCount:  10,
		codeLength: 8,
	}
}

// GenerateCodes generates a set of recovery codes
func (m *RecoveryCodeManager) GenerateCodes() ([]string, error) {
	codes := make([]string, m.codeCount)
	for i := 0; i < m.codeCount; i++ {
		code, err := crypto.GenerateRandomString(m.codeLength)
		if err != nil {
			return nil, fmt.Errorf("failed to generate recovery code: %w", err)
		}
		// Format as XXXX-XXXX for readability
		codes[i] = strings.ToUpper(code[:4] + "-" + code[4:])
	}
	return codes, nil
}

// HashCodes hashes recovery codes for storage
func (m *RecoveryCodeManager) HashCodes(codes []string, hasher *crypto.Argon2Hasher) ([]string, error) {
	hashedCodes := make([]string, len(codes))
	for i, code := range codes {
		// Remove formatting
		cleanCode := strings.ReplaceAll(code, "-", "")
		hash, err := hasher.Hash(cleanCode)
		if err != nil {
			return nil, fmt.Errorf("failed to hash recovery code: %w", err)
		}
		hashedCodes[i] = hash
	}
	return hashedCodes, nil
}

// VerifyCode verifies a recovery code against hashed codes
func (m *RecoveryCodeManager) VerifyCode(code string, hashedCodes []string, hasher *crypto.Argon2Hasher) (int, bool, error) {
	// Remove formatting
	cleanCode := strings.ToUpper(strings.ReplaceAll(code, "-", ""))

	for i, hashedCode := range hashedCodes {
		if hashedCode == "" {
			continue // Code already used
		}
		valid, err := hasher.Verify(cleanCode, hashedCode)
		if err != nil {
			continue // Invalid hash format, skip
		}
		if valid {
			return i, true, nil
		}
	}

	return -1, false, nil
}
