package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	// MinJWTSecretLength is the minimum required length for JWT secret (256 bits)
	MinJWTSecretLength = 32
)

// Config holds all configuration for the application
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Security SecurityConfig `yaml:"security"`
	XMPP     XMPPConfig     `yaml:"xmpp"`
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Address string    `yaml:"address"`
	TLS     TLSConfig `yaml:"tls"`
}

// TLSConfig holds TLS configuration
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	Driver          string `yaml:"driver"` // sqlite or postgres
	DSN             string `yaml:"dsn"`
	EncryptionKey   string `yaml:"encryption_key"`    // Base64 encoded 32-byte key
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

// SecurityConfig holds security-related configuration
type SecurityConfig struct {
	JWT       JWTConfig       `yaml:"jwt"`
	MFA       MFAConfig       `yaml:"mfa"`
	Password  PasswordConfig  `yaml:"password"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	CORS      CORSConfig      `yaml:"cors"`
	Cookies   CookieConfig    `yaml:"cookies"`
}

// CookieConfig controls how auth cookies (xmpanel_refresh, csrf_token) are
// emitted. By default, the Secure flag tracks `server.tls.enabled` — but in
// production deployments where nginx terminates TLS and XMPanel itself runs
// HTTP on loopback, that derivation gives the wrong answer. Use SecureOverride
// to force the flag.
//
//   - "auto"   (default): mirror server.tls.enabled
//   - "always": always set Secure (correct for nginx-fronted deployments)
//   - "never":  never set Secure (only for local dev over plain HTTP)
type CookieConfig struct {
	SecureOverride string `yaml:"secure_override"`
}

// JWTConfig holds JWT configuration
type JWTConfig struct {
	Secret           string        `yaml:"secret"`
	AccessTokenTTL   time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL  time.Duration `yaml:"refresh_token_ttl"`
	Issuer           string        `yaml:"issuer"`
}

// MFAConfig holds MFA configuration
type MFAConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Issuer     string `yaml:"issuer"`
	Required   bool   `yaml:"required"` // If true, all users must enable MFA
}

// PasswordConfig holds password policy configuration
type PasswordConfig struct {
	MinLength       int           `yaml:"min_length"`
	RequireUpper    bool          `yaml:"require_upper"`
	RequireLower    bool          `yaml:"require_lower"`
	RequireNumber   bool          `yaml:"require_number"`
	RequireSpecial  bool          `yaml:"require_special"`
	Argon2Time      uint32        `yaml:"argon2_time"`
	Argon2Memory    uint32        `yaml:"argon2_memory"`
	Argon2Threads   uint8         `yaml:"argon2_threads"`
	MaxLoginAttempts int          `yaml:"max_login_attempts"`
	LockoutDuration time.Duration `yaml:"lockout_duration"`
}

// RateLimitConfig holds rate limiting configuration
type RateLimitConfig struct {
	Enabled           bool          `yaml:"enabled"`
	RequestsPerSecond float64       `yaml:"requests_per_second"`
	Burst             int           `yaml:"burst"`
	LoginAttempts     int           `yaml:"login_attempts"`
	LoginWindow       time.Duration `yaml:"login_window"`
	TrustedProxies    []string      `yaml:"trusted_proxies"`    // List of trusted proxy IPs/CIDRs
	TrustXForwardedFor bool         `yaml:"trust_x_forwarded_for"` // If false, always use RemoteAddr
}

// CORSConfig holds CORS configuration
type CORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins"`
	AllowedMethods   []string `yaml:"allowed_methods"`
	AllowedHeaders   []string `yaml:"allowed_headers"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	MaxAge           int      `yaml:"max_age"`
}

// XMPPConfig holds XMPP servers configuration
type XMPPConfig struct {
	Servers []XMPPServerConfig `yaml:"servers"`
}

// XMPPServerConfig holds individual XMPP server configuration
type XMPPServerConfig struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Type     string `yaml:"type"` // prosody or ejabberd
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	APIKey   string `yaml:"api_key"` // Will be encrypted at rest
	TLS      bool   `yaml:"tls"`
	Enabled  bool   `yaml:"enabled"`
}

// Load loads configuration from file
func Load() (*Config, error) {
	configPath := os.Getenv("XMPANEL_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply defaults for missing values
	applyDefaults(&cfg)

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// CookieSecure decides whether auth cookies get the Secure attribute,
// applying the security.cookies.secure_override setting on top of the
// derived-from-TLS default.
func (c *Config) CookieSecure() bool {
	switch c.Security.Cookies.SecureOverride {
	case "always":
		return true
	case "never":
		return false
	}
	return c.Server.TLS.Enabled
}

// Validate validates the configuration and returns an error if invalid
func (c *Config) Validate() error {
	// Validate JWT secret
	if c.Security.JWT.Secret == "" {
		// Generate a random secret and warn user
		secret, err := generateRandomSecret(MinJWTSecretLength)
		if err != nil {
			return fmt.Errorf("failed to generate JWT secret: %w", err)
		}
		c.Security.JWT.Secret = secret
		log.Printf("WARNING: No JWT secret configured. Generated random secret. " +
			"Sessions will be invalidated on restart. Set security.jwt.secret in config for persistence.")
	} else if len(c.Security.JWT.Secret) < MinJWTSecretLength {
		return errors.New("JWT secret must be at least 32 characters (256 bits) for security")
	}

	// Validate database encryption key
	if c.Database.EncryptionKey == "" {
		key, err := generateRandomSecret(32)
		if err != nil {
			return fmt.Errorf("failed to generate encryption key: %w", err)
		}
		c.Database.EncryptionKey = base64.StdEncoding.EncodeToString([]byte(key))
		log.Printf("WARNING: No database encryption key configured. Generated random key. " +
			"Encrypted data will be unreadable after restart. Set database.encryption_key in config for persistence.")
	}

	// Validate CORS configuration - disallow credentials with wildcard origin
	for _, origin := range c.Security.CORS.AllowedOrigins {
		if origin == "*" && c.Security.CORS.AllowCredentials {
			return errors.New("CORS: cannot use wildcard origin (*) with allow_credentials=true")
		}
	}

	// Validate cookies.secure_override
	switch c.Security.Cookies.SecureOverride {
	case "", "auto", "always", "never":
		// ok
	default:
		return fmt.Errorf("security.cookies.secure_override must be one of auto/always/never, got %q",
			c.Security.Cookies.SecureOverride)
	}

	return nil
}

// generateRandomSecret generates a cryptographically secure random string
func generateRandomSecret(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	cfg := &Config{}
	applyDefaults(cfg)
	return cfg
}

func applyDefaults(cfg *Config) {
	// Server defaults
	if cfg.Server.Address == "" {
		cfg.Server.Address = ":8080"
	}

	// Database defaults (PostgreSQL)
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "postgres"
	}
	if cfg.Database.DSN == "" {
		cfg.Database.DSN = "host=localhost port=5432 user=xmpanel password=xmpanel dbname=xmpanel sslmode=disable"
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = 25
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 5
	}
	if cfg.Database.ConnMaxLifetime == "" {
		cfg.Database.ConnMaxLifetime = "5m"
	}

	// JWT defaults
	if cfg.Security.JWT.AccessTokenTTL == 0 {
		cfg.Security.JWT.AccessTokenTTL = 15 * time.Minute
	}
	if cfg.Security.JWT.RefreshTokenTTL == 0 {
		cfg.Security.JWT.RefreshTokenTTL = 7 * 24 * time.Hour
	}
	if cfg.Security.JWT.Issuer == "" {
		cfg.Security.JWT.Issuer = "xmpanel"
	}

	// MFA defaults
	if cfg.Security.MFA.Issuer == "" {
		cfg.Security.MFA.Issuer = "XMPanel"
	}

	// Password defaults
	if cfg.Security.Password.MinLength == 0 {
		cfg.Security.Password.MinLength = 12
	}
	if cfg.Security.Password.Argon2Time == 0 {
		cfg.Security.Password.Argon2Time = 3
	}
	if cfg.Security.Password.Argon2Memory == 0 {
		cfg.Security.Password.Argon2Memory = 64 * 1024 // 64MB
	}
	if cfg.Security.Password.Argon2Threads == 0 {
		cfg.Security.Password.Argon2Threads = 4
	}
	if cfg.Security.Password.MaxLoginAttempts == 0 {
		cfg.Security.Password.MaxLoginAttempts = 5
	}
	if cfg.Security.Password.LockoutDuration == 0 {
		cfg.Security.Password.LockoutDuration = 15 * time.Minute
	}

	// Rate limit defaults
	if cfg.Security.RateLimit.RequestsPerSecond == 0 {
		cfg.Security.RateLimit.RequestsPerSecond = 100
	}
	if cfg.Security.RateLimit.Burst == 0 {
		cfg.Security.RateLimit.Burst = 200
	}
	if cfg.Security.RateLimit.LoginAttempts == 0 {
		cfg.Security.RateLimit.LoginAttempts = 5
	}
	if cfg.Security.RateLimit.LoginWindow == 0 {
		cfg.Security.RateLimit.LoginWindow = 15 * time.Minute
	}

	// CORS defaults
	if len(cfg.Security.CORS.AllowedMethods) == 0 {
		cfg.Security.CORS.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	}
	if len(cfg.Security.CORS.AllowedHeaders) == 0 {
		cfg.Security.CORS.AllowedHeaders = []string{"Authorization", "Content-Type", "X-CSRF-Token", "X-Request-ID"}
	}
	if cfg.Security.CORS.MaxAge == 0 {
		cfg.Security.CORS.MaxAge = 86400
	}
}
