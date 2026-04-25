package crypto

import (
	"strings"
	"testing"
)

// generateTestKey returns a deterministic-shaped (but random) 32-byte key
// encoded as base64, suitable for NewKeyRing.
func generateTestKey(t *testing.T) string {
	t.Helper()
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return key
}

func TestArgon2Hasher_HashVerify(t *testing.T) {
	// Use minimal Argon2 parameters for fast tests; production values would be
	// far higher.
	h := NewArgon2Hasher(1, 8*1024, 1)

	hash, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash missing argon2id prefix: %q", hash)
	}

	ok, err := h.Verify("correct horse battery staple", hash)
	if err != nil || !ok {
		t.Errorf("Verify(correct password): ok=%v err=%v", ok, err)
	}

	ok, err = h.Verify("wrong password", hash)
	if err != nil {
		t.Errorf("Verify(wrong password) returned error: %v", err)
	}
	if ok {
		t.Error("Verify(wrong password) returned true, expected false")
	}
}

func TestArgon2Hasher_RehashEachCall(t *testing.T) {
	h := NewArgon2Hasher(1, 8*1024, 1)
	a, _ := h.Hash("same password")
	b, _ := h.Hash("same password")
	if a == b {
		t.Error("two hashes of same password matched — salt is not random")
	}
}

func TestArgon2Hasher_VerifyMalformed(t *testing.T) {
	h := NewArgon2Hasher(1, 8*1024, 1)
	if _, err := h.Verify("x", "not-a-valid-hash"); err == nil {
		t.Error("Verify on malformed hash should error")
	}
}

func TestKeyRing_EncryptDecryptRoundtrip(t *testing.T) {
	kr, err := NewKeyRing(generateTestKey(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	plaintext := "sensitive api key value"
	ciphertext, err := kr.EncryptString(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == plaintext {
		t.Error("ciphertext equals plaintext")
	}

	got, err := kr.DecryptString(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != plaintext {
		t.Errorf("Decrypt: got %q, want %q", got, plaintext)
	}
}

func TestKeyRing_RotationDecryptsOldCiphertext(t *testing.T) {
	// Encrypt with key A, rotate to key B, ensure A's ciphertext still decrypts.
	kr, err := NewKeyRing(generateTestKey(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	oldCipher, err := kr.EncryptString("payload-v1")
	if err != nil {
		t.Fatalf("Encrypt with key A: %v", err)
	}

	newKeyID, err := kr.AddKey(generateTestKey(t))
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := kr.SetCurrentKey(newKeyID); err != nil {
		t.Fatalf("SetCurrentKey: %v", err)
	}

	// New encryption should use the new key (different keyID prefix)
	newCipher, err := kr.EncryptString("payload-v2")
	if err != nil {
		t.Fatalf("Encrypt with key B: %v", err)
	}
	if oldPrefix, newPrefix := keyIDOf(oldCipher), keyIDOf(newCipher); oldPrefix == newPrefix {
		t.Errorf("rotation didn't change key id: both ciphertexts use %q", oldPrefix)
	}

	// Both ciphertexts decrypt
	if got, _ := kr.DecryptString(oldCipher); got != "payload-v1" {
		t.Errorf("old ciphertext: got %q", got)
	}
	if got, _ := kr.DecryptString(newCipher); got != "payload-v2" {
		t.Errorf("new ciphertext: got %q", got)
	}
}

func keyIDOf(ciphertext string) string {
	if i := strings.Index(ciphertext, ":"); i >= 0 {
		return ciphertext[:i]
	}
	return ""
}

func TestKeyRing_RejectsWrongKeyLength(t *testing.T) {
	// 16 bytes (AES-128) should be rejected — we require AES-256.
	short := "MTIzNDU2Nzg5MDEyMzQ1Ng==" // 16 bytes base64
	if _, err := NewKeyRing(short); err == nil {
		t.Error("NewKeyRing accepted a 16-byte key")
	}
}

func TestKeyRing_DecryptUnknownKeyID(t *testing.T) {
	kr, err := NewKeyRing(generateTestKey(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	if _, err := kr.DecryptString("unknown-keyid:somebase64data"); err == nil {
		t.Error("Decrypt with unknown key id should fail")
	}
}

func TestKeyRing_DecryptTamperedCiphertext(t *testing.T) {
	kr, err := NewKeyRing(generateTestKey(t))
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}
	cipher, err := kr.EncryptString("hello")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Flip one base64 char in the body (after the colon).
	idx := strings.LastIndex(cipher, ":") + 1
	if idx <= 0 || idx >= len(cipher) {
		t.Fatalf("unexpected ciphertext format: %q", cipher)
	}
	tampered := cipher[:idx] + flipFirstByte(cipher[idx:])
	if _, err := kr.DecryptString(tampered); err == nil {
		t.Error("Decrypt of tampered ciphertext should fail (GCM auth)")
	}
}

func flipFirstByte(s string) string {
	if s == "" {
		return s
	}
	c := s[0]
	if c == 'A' {
		c = 'B'
	} else {
		c = 'A'
	}
	return string(c) + s[1:]
}

func TestGenerateRandomBytes_Length(t *testing.T) {
	for _, n := range []int{1, 16, 32, 64} {
		b, err := GenerateRandomBytes(n)
		if err != nil {
			t.Errorf("GenerateRandomBytes(%d): %v", n, err)
			continue
		}
		if len(b) != n {
			t.Errorf("GenerateRandomBytes(%d) returned %d bytes", n, len(b))
		}
	}
}

func TestGenerateRandomString_Length(t *testing.T) {
	s, err := GenerateRandomString(24)
	if err != nil {
		t.Fatalf("GenerateRandomString: %v", err)
	}
	if len(s) != 24 {
		t.Errorf("len(s)=%d, want 24", len(s))
	}
}
