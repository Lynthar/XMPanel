package store

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
)

// BearerCredentials wraps a plain token, the only credential kind so far.
func BearerCredentials(token string) adapter.Credentials {
	return adapter.Credentials{Kind: adapter.CredentialsBearer, Token: token}
}

// EncryptCredentials serialises creds as JSON and encrypts it for the
// servers.credentials_encrypted column.
func EncryptCredentials(keyRing *crypto.KeyRing, creds adapter.Credentials) (string, error) {
	if keyRing == nil {
		return "", errors.New("encryption key ring is not configured")
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return "", err
	}
	return keyRing.EncryptString(string(raw))
}

// DecryptCredentials reverses EncryptCredentials. An empty ciphertext yields
// empty credentials, which is how a row registered without a token reads.
func DecryptCredentials(keyRing *crypto.KeyRing, ciphertext string) (adapter.Credentials, error) {
	if ciphertext == "" {
		return adapter.Credentials{Kind: adapter.CredentialsBearer}, nil
	}
	if keyRing == nil {
		return adapter.Credentials{}, errors.New("encryption key ring is not configured")
	}
	raw, err := keyRing.DecryptString(ciphertext)
	if err != nil {
		return adapter.Credentials{}, fmt.Errorf("decrypt credentials: %w", err)
	}
	var creds adapter.Credentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return adapter.Credentials{}, fmt.Errorf("decode credentials: %w", err)
	}
	return creds, nil
}

// MigrateServerCredentials re-encodes rows registered before credentials
// became JSON: each legacy api_key_encrypted value is decrypted, wrapped as a
// bearer credential, written to credentials_encrypted, and the legacy column
// is cleared. A value that no longer decrypts aborts startup rather than being
// silently dropped, because the token cannot be recovered afterwards.
func MigrateServerCredentials(db *DB, keyRing *crypto.KeyRing) error {
	var hasLegacy bool
	err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'servers' AND column_name = 'api_key_encrypted'
	)`).Scan(&hasLegacy)
	if err != nil || !hasLegacy {
		return err
	}
	rows, err := db.Query(`SELECT id, api_key_encrypted FROM servers WHERE api_key_encrypted IS NOT NULL AND api_key_encrypted <> ''`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	type legacy struct {
		id  int64
		key string
	}
	var pending []legacy
	for rows.Next() {
		var row legacy
		if err := rows.Scan(&row.id, &row.key); err != nil {
			return err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range pending {
		token, err := keyRing.DecryptString(row.key)
		if err != nil {
			return fmt.Errorf("server %d: legacy API key does not decrypt with database.encryption_key: %w", row.id, err)
		}
		encoded, err := EncryptCredentials(keyRing, BearerCredentials(token))
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE servers SET credentials_encrypted = $1, api_key_encrypted = NULL WHERE id = $2`, encoded, row.id); err != nil {
			return err
		}
	}
	return nil
}
