// Package storetest opens the PostgreSQL database named by XMPANEL_TEST_DSN
// for integration tests, migrated and with a fresh key ring.
package storetest

import (
	"os"
	"testing"

	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
)

// NewDB skips the test when XMPANEL_TEST_DSN is unset; CI sets it, and its
// "no test may skip" step keeps the skip from hiding there.
func NewDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := os.Getenv("XMPANEL_TEST_DSN")
	if dsn == "" {
		t.Skip("XMPANEL_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	db, err := store.NewDB(config.DatabaseConfig{DSN: dsn, MaxOpenConns: 10, MaxIdleConns: 5})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func NewKeyRing(t *testing.T) *crypto.KeyRing {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// InsertServer registers a backend row and removes it when the test ends.
func InsertServer(t *testing.T, db *store.DB, ring *crypto.KeyRing, impl, endpoint, domain, token string) int64 {
	t.Helper()
	encrypted, err := store.EncryptCredentials(ring, store.BearerCredentials(token))
	if err != nil {
		t.Fatal(err)
	}
	protocol := "xmpp"
	if impl == "synapse" || impl == "tuwunel" {
		protocol = "matrix"
	}
	var id int64
	err = db.QueryRow(`INSERT INTO servers (name, protocol, implementation, endpoint, domain, credentials_encrypted)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, "test "+impl, protocol, impl, endpoint, domain, encrypted).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM servers WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	return id
}
