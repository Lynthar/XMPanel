package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/xmpanel/xmpanel/internal/config"

	_ "github.com/lib/pq"
)

// DB wraps the database connection
type DB struct {
	*sql.DB
}

// NewDB creates a new database connection
func NewDB(cfg config.DatabaseConfig) (*DB, error) {
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)

	if cfg.ConnMaxLifetime != "" {
		duration, err := time.ParseDuration(cfg.ConnMaxLifetime)
		if err == nil {
			db.SetConnMaxLifetime(duration)
		}
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &DB{db}, nil
}

// PasswordHasher is a function type for hashing passwords
type PasswordHasher func(password string) (string, error)

// InitResult contains the result of database initialization
type InitResult struct {
	AdminCreated  bool
	AdminUsername string
	AdminPassword string // Only set if newly generated
}

// EnsureInitialAdmin creates the initial superadmin if no users exist
// Returns the credentials if a new admin was created
func EnsureInitialAdmin(db *DB, hasher PasswordHasher) (*InitResult, error) {
	result := &InitResult{}

	// Check if any users exist
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("failed to check users: %w", err)
	}

	if count > 0 {
		// Users exist, nothing to do
		return result, nil
	}

	// Generate a secure random password
	password, err := generateSecurePassword(16)
	if err != nil {
		return nil, fmt.Errorf("failed to generate password: %w", err)
	}

	// Hash the password
	passwordHash, err := hasher(password)
	if err != nil {
		return nil, fmt.Errorf("failed to hash password: %w", err)
	}

	// Create the initial superadmin
	_, err = db.Exec(`
		INSERT INTO users (username, email, password_hash, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, "admin", "admin@localhost", passwordHash, "superadmin", time.Now(), time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to create initial admin: %w", err)
	}

	result.AdminCreated = true
	result.AdminUsername = "admin"
	result.AdminPassword = password

	log.Printf("========================================")
	log.Printf("INITIAL ADMIN ACCOUNT CREATED")
	log.Printf("Username: %s", result.AdminUsername)
	log.Printf("Password: %s", result.AdminPassword)
	log.Printf("IMPORTANT: Change this password immediately!")
	log.Printf("========================================")

	return result, nil
}

// generateSecurePassword generates a cryptographically secure password
func generateSecurePassword(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	// Use URL-safe base64 encoding and take the first 'length' characters
	password := base64.URLEncoding.EncodeToString(bytes)
	if len(password) > length {
		password = password[:length]
	}
	return password, nil
}

// Migrate runs database migrations
func Migrate(db *DB) error {
	migrations := []string{
		// Users table
		`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			username VARCHAR(255) UNIQUE NOT NULL,
			email VARCHAR(255) UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role VARCHAR(50) NOT NULL DEFAULT 'viewer',
			mfa_enabled BOOLEAN NOT NULL DEFAULT FALSE,
			mfa_secret TEXT,
			recovery_codes TEXT,
			failed_login_attempts INTEGER NOT NULL DEFAULT 0,
			locked_until TIMESTAMP,
			last_login_at TIMESTAMP,
			last_login_ip VARCHAR(45),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Sessions table
		`CREATE TABLE IF NOT EXISTS sessions (
			id SERIAL PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			session_id VARCHAR(255) UNIQUE NOT NULL,
			device_id VARCHAR(255),
			device_info TEXT,
			ip_address VARCHAR(45),
			user_agent TEXT,
			refresh_token_hash TEXT,
			expires_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_used_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// XMPP Servers table
		`CREATE TABLE IF NOT EXISTS xmpp_servers (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			type VARCHAR(50) NOT NULL,
			host VARCHAR(255) NOT NULL,
			port INTEGER NOT NULL,
			api_key_encrypted TEXT,
			tls_enabled BOOLEAN NOT NULL DEFAULT TRUE,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(host, port)
		)`,

		// Proxy Servers table
		`CREATE TABLE IF NOT EXISTS proxy_servers (
			id SERIAL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			type VARCHAR(50) NOT NULL,
			host VARCHAR(255) NOT NULL,
			port INTEGER NOT NULL,
			stats_endpoint TEXT,
			auth_user VARCHAR(255),
			auth_password_encrypted TEXT,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(host, port)
		)`,

		// Audit Logs table (with chain hash for integrity)
		// details is JSONB so callers can filter by structured fields via @>.
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id SERIAL PRIMARY KEY,
			user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			username VARCHAR(255),
			action VARCHAR(100) NOT NULL,
			resource_type VARCHAR(100),
			resource_id VARCHAR(255),
			details JSONB,
			ip_address VARCHAR(45),
			user_agent TEXT,
			request_id VARCHAR(255),
			prev_hash VARCHAR(64),
			hash VARCHAR(64) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Upgrade existing TEXT details columns to JSONB. The USING clause
		// rewrites empty strings (legacy nil-details encoding) to NULL and
		// parses the rest. Idempotent: only runs if the column is still TEXT.
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				  AND table_name = 'audit_logs'
				  AND column_name = 'details'
				  AND data_type = 'text'
			) THEN
				ALTER TABLE audit_logs
				ALTER COLUMN details TYPE JSONB
				USING (CASE
					WHEN details IS NULL OR details = '' THEN NULL
					ELSE details::jsonb
				END);
			END IF;
		END $$`,

		// Settings table (key-value store for system settings)
		`CREATE TABLE IF NOT EXISTS settings (
			key VARCHAR(255) PRIMARY KEY,
			value TEXT NOT NULL,
			encrypted BOOLEAN NOT NULL DEFAULT FALSE,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,

		// Create indexes
		`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_session_id ON sessions(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action)`,
	}

	for _, migration := range migrations {
		if _, err := db.Exec(migration); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	return nil
}
