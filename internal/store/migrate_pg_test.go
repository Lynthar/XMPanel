package store

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
)

// legacyDatabase creates a throwaway database holding the pre-multi-protocol
// schema with one registered Prosody server, and returns a DB connected to it.
func legacyDatabase(t *testing.T, ring *crypto.KeyRing) *DB {
	t.Helper()
	dsn := os.Getenv("XMPANEL_TEST_DSN")
	if dsn == "" {
		t.Skip("XMPANEL_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	const name = "xmpanel_test_legacy"
	if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name); err != nil {
		t.Fatalf("drop legacy database: %v", err)
	}
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatalf("create legacy database: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP DATABASE IF EXISTS ` + name) })

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := NewDB(config.DatabaseConfig{DSN: u.String(), MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacy := []string{
		`CREATE TABLE users (id SERIAL PRIMARY KEY, username VARCHAR(255) UNIQUE NOT NULL, email VARCHAR(255) UNIQUE NOT NULL,
			password_hash TEXT NOT NULL, role VARCHAR(50) NOT NULL DEFAULT 'viewer', mfa_enabled BOOLEAN NOT NULL DEFAULT FALSE,
			mfa_secret TEXT, recovery_codes TEXT, failed_login_attempts INTEGER NOT NULL DEFAULT 0, locked_until TIMESTAMP,
			last_login_at TIMESTAMP, last_login_ip VARCHAR(45), created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE xmpp_servers (id SERIAL PRIMARY KEY, name VARCHAR(255) NOT NULL, type VARCHAR(50) NOT NULL,
			host VARCHAR(255) NOT NULL, port INTEGER NOT NULL, api_key_encrypted TEXT, tls_enabled BOOLEAN NOT NULL DEFAULT TRUE,
			enabled BOOLEAN NOT NULL DEFAULT TRUE, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(host, port))`,
	}
	for _, stmt := range legacy {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("legacy schema: %v", err)
		}
	}
	token, err := ring.EncryptString("legacy-bearer-token")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO xmpp_servers (name, type, host, port, api_key_encrypted, tls_enabled, enabled)
		VALUES ('old prosody', 'prosody', 'xmpp.example.com', 5280, $1, FALSE, TRUE),
		       ('old ejabberd', 'ejabberd', 'chat.example.org', 5443, NULL, TRUE, FALSE)`, token)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func newRing(t *testing.T) *crypto.KeyRing {
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

// An install from before the servers table keeps its rows: the table is
// renamed, endpoint and domain are derived from host, port and tls_enabled,
// and the bearer token moves into the credentials JSON. Running everything
// twice must change nothing.
func TestMigrateUpgradesLegacyServers(t *testing.T) {
	ring := newRing(t)
	db := legacyDatabase(t, ring)
	for round := 1; round <= 2; round++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("round %d migrate: %v", round, err)
		}
		if err := MigrateServerCredentials(db, ring); err != nil {
			t.Fatalf("round %d credentials: %v", round, err)
		}
	}

	var protocol, impl, endpoint, domain string
	var enabled bool
	var credentials, legacyKey sql.NullString
	err := db.QueryRow(`SELECT protocol, implementation, endpoint, domain, enabled, credentials_encrypted, api_key_encrypted
		FROM servers WHERE name = 'old prosody'`).Scan(&protocol, &impl, &endpoint, &domain, &enabled, &credentials, &legacyKey)
	if err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if protocol != "xmpp" || impl != "prosody" || endpoint != "http://xmpp.example.com:5280" || domain != "xmpp.example.com" || !enabled {
		t.Fatalf("migrated row = %s %s %s %s %v", protocol, impl, endpoint, domain, enabled)
	}
	if legacyKey.Valid {
		t.Fatal("legacy api_key_encrypted was not cleared")
	}
	creds, err := DecryptCredentials(ring, credentials.String)
	if err != nil || creds.Kind != "bearer" || creds.Token != "legacy-bearer-token" {
		t.Fatalf("credentials = %+v, %v", creds, err)
	}

	err = db.QueryRow(`SELECT endpoint, credentials_encrypted FROM servers WHERE name = 'old ejabberd'`).Scan(&endpoint, &credentials)
	if err != nil || endpoint != "https://chat.example.org:5443" || credentials.Valid {
		t.Fatalf("tls row = %s %v, %v", endpoint, credentials, err)
	}

	var xmppServers bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'xmpp_servers')`).Scan(&xmppServers); err != nil || xmppServers {
		t.Fatalf("xmpp_servers still exists: %v %v", xmppServers, err)
	}
	if _, err := db.Exec(`INSERT INTO servers (name, protocol, implementation, endpoint, domain) VALUES ('dup', 'xmpp', 'prosody', 'http://xmpp.example.com:5280', 'xmpp.example.com')`); err == nil || !strings.Contains(err.Error(), "idx_servers_endpoint_domain") {
		t.Fatalf("endpoint+domain uniqueness is not enforced: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO servers (name, protocol, implementation, endpoint, domain) VALUES ('new', 'xmpp', 'ejabberd', 'http://127.0.0.1:5280', 'new.example.com')`); err != nil {
		t.Fatalf("a row without legacy columns must insert: %v", err)
	}
}

// A legacy token encrypted under a different key must stop startup rather
// than be dropped.
func TestMigrateServerCredentialsRefusesUndecryptableTokens(t *testing.T) {
	db := legacyDatabase(t, newRing(t))
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	err := MigrateServerCredentials(db, newRing(t))
	if err == nil || !strings.Contains(err.Error(), "does not decrypt") {
		t.Fatalf("wrong key accepted: %v", err)
	}
}
