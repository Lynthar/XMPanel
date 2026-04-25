package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidKey        = errors.New("invalid encryption key")
	ErrInvalidCiphertext = errors.New("invalid ciphertext")
	ErrDecryptionFailed  = errors.New("decryption failed")
)

// KeyRing manages encryption keys with support for key rotation
type KeyRing struct {
	mu         sync.RWMutex
	keys       map[string][]byte
	currentKey string
}

// NewKeyRing creates a new KeyRing with the given primary key
func NewKeyRing(primaryKeyBase64 string) (*KeyRing, error) {
	key, err := base64.StdEncoding.DecodeString(primaryKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key: %w", err)
	}

	if len(key) != 32 {
		return nil, ErrInvalidKey
	}

	keyID := generateKeyID(key)
	return &KeyRing{
		keys:       map[string][]byte{keyID: key},
		currentKey: keyID,
	}, nil
}

// AddKey adds a new key to the keyring
func (kr *KeyRing) AddKey(keyBase64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode key: %w", err)
	}

	if len(key) != 32 {
		return "", ErrInvalidKey
	}

	keyID := generateKeyID(key)

	kr.mu.Lock()
	defer kr.mu.Unlock()
	kr.keys[keyID] = key

	return keyID, nil
}

// SetCurrentKey sets the current key for encryption
func (kr *KeyRing) SetCurrentKey(keyID string) error {
	kr.mu.RLock()
	defer kr.mu.RUnlock()

	if _, ok := kr.keys[keyID]; !ok {
		return ErrInvalidKey
	}
	kr.currentKey = keyID
	return nil
}

// Encrypt encrypts plaintext using AES-256-GCM
func (kr *KeyRing) Encrypt(plaintext []byte) (string, error) {
	kr.mu.RLock()
	key := kr.keys[kr.currentKey]
	keyID := kr.currentKey
	kr.mu.RUnlock()

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	// Format: keyID:base64(nonce+ciphertext)
	encoded := base64.StdEncoding.EncodeToString(ciphertext)
	return fmt.Sprintf("%s:%s", keyID, encoded), nil
}

// Decrypt decrypts ciphertext using AES-256-GCM
func (kr *KeyRing) Decrypt(encryptedData string) ([]byte, error) {
	parts := strings.SplitN(encryptedData, ":", 2)
	if len(parts) != 2 {
		return nil, ErrInvalidCiphertext
	}

	keyID := parts[0]
	ciphertext, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	kr.mu.RLock()
	key, ok := kr.keys[keyID]
	kr.mu.RUnlock()

	if !ok {
		return nil, ErrInvalidKey
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrInvalidCiphertext
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}

// EncryptString is a convenience method for encrypting strings
func (kr *KeyRing) EncryptString(plaintext string) (string, error) {
	return kr.Encrypt([]byte(plaintext))
}

// DecryptString is a convenience method for decrypting to strings
func (kr *KeyRing) DecryptString(ciphertext string) (string, error) {
	plaintext, err := kr.Decrypt(ciphertext)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// generateKeyID generates a short ID for a key
func generateKeyID(key []byte) string {
	hash := sha256.Sum256(key)
	return base64.RawURLEncoding.EncodeToString(hash[:8])
}

// Argon2Hasher handles password hashing using Argon2id
type Argon2Hasher struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

// NewArgon2Hasher creates a new Argon2id hasher with the given parameters
func NewArgon2Hasher(time, memory uint32, threads uint8) *Argon2Hasher {
	return &Argon2Hasher{
		time:    time,
		memory:  memory,
		threads: threads,
		keyLen:  32,
		saltLen: 16,
	}
}

// Hash hashes a password using Argon2id
func (h *Argon2Hasher) Hash(password string) (string, error) {
	salt := make([]byte, h.saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, h.time, h.memory, h.threads, h.keyLen)

	// Format: $argon2id$v=19$m=memory,t=time,p=threads$salt$hash
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.memory, h.time, h.threads, b64Salt, b64Hash), nil
}

// Verify verifies a password against a hash
func (h *Argon2Hasher) Verify(password, encodedHash string) (bool, error) {
	// Parse the encoded hash
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return false, errors.New("invalid hash format")
	}

	if parts[1] != "argon2id" {
		return false, errors.New("unsupported algorithm")
	}

	var version int
	_, err := fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil {
		return false, fmt.Errorf("failed to parse version: %w", err)
	}

	var memory, time uint32
	var threads uint8
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads)
	if err != nil {
		return false, fmt.Errorf("failed to parse parameters: %w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("failed to decode salt: %w", err)
	}

	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("failed to decode hash: %w", err)
	}

	// Compute hash with same parameters
	computedHash := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(expectedHash)))

	// Constant-time comparison
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1, nil
}

// GenerateRandomBytes generates cryptographically secure random bytes
func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	return b, nil
}

// GenerateRandomString generates a cryptographically secure random string
func GenerateRandomString(n int) (string, error) {
	bytes, err := GenerateRandomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes)[:n], nil
}

// GenerateKey generates a new 256-bit encryption key
func GenerateKey() (string, error) {
	key, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// HashToken returns a base64url-encoded SHA-256 hash of a token, suitable for
// storing refresh-token identifiers. Collision resistance is what we need here
// — preimage resistance is incidental — so SHA-256 (not Argon2) is appropriate.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
