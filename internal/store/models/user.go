package models

import (
	"database/sql"
	"time"
)

// Role represents a user role
type Role string

const (
	RoleSuperAdmin Role = "superadmin"
	RoleAdmin      Role = "admin"
	RoleOperator   Role = "operator"
	RoleViewer     Role = "viewer"
	RoleAuditor    Role = "auditor"
)

// User represents a system user
type User struct {
	ID                  int64          `json:"id" db:"id"`
	Username            string         `json:"username" db:"username"`
	Email               string         `json:"email" db:"email"`
	PasswordHash        string         `json:"-" db:"password_hash"`
	Role                Role           `json:"role" db:"role"`
	MFAEnabled          bool           `json:"mfa_enabled" db:"mfa_enabled"`
	MFASecret           sql.NullString `json:"-" db:"mfa_secret"`
	RecoveryCodes       sql.NullString `json:"-" db:"recovery_codes"`
	FailedLoginAttempts int            `json:"-" db:"failed_login_attempts"`
	LockedUntil         sql.NullTime   `json:"locked_until,omitempty" db:"locked_until"`
	LastLoginAt         sql.NullTime   `json:"last_login_at,omitempty" db:"last_login_at"`
	LastLoginIP         sql.NullString `json:"last_login_ip,omitempty" db:"last_login_ip"`
	CreatedAt           time.Time      `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at" db:"updated_at"`
}

// IsLocked returns true if the user account is currently locked
func (u *User) IsLocked() bool {
	if !u.LockedUntil.Valid {
		return false
	}
	return time.Now().Before(u.LockedUntil.Time)
}

// Session represents a user session
type Session struct {
	ID               int64          `json:"id" db:"id"`
	UserID           int64          `json:"user_id" db:"user_id"`
	SessionID        string         `json:"session_id" db:"session_id"`
	DeviceID         sql.NullString `json:"device_id,omitempty" db:"device_id"`
	DeviceInfo       sql.NullString `json:"device_info,omitempty" db:"device_info"`
	IPAddress        sql.NullString `json:"ip_address,omitempty" db:"ip_address"`
	UserAgent        sql.NullString `json:"user_agent,omitempty" db:"user_agent"`
	RefreshTokenHash sql.NullString `json:"-" db:"refresh_token_hash"`
	ExpiresAt        time.Time      `json:"expires_at" db:"expires_at"`
	CreatedAt        time.Time      `json:"created_at" db:"created_at"`
	LastUsedAt       time.Time      `json:"last_used_at" db:"last_used_at"`
}

// CreateUserRequest represents a request to create a user
type CreateUserRequest struct {
	Username string `json:"username" validate:"required,min=3,max=32,alphanum"`
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=12,max=128"`
	Role     Role   `json:"role" validate:"required,oneof=admin operator viewer auditor"`
}

// UpdateUserRequest represents a request to update a user
type UpdateUserRequest struct {
	Email    *string `json:"email,omitempty" validate:"omitempty,email"`
	Password *string `json:"password,omitempty" validate:"omitempty,min=12,max=128"`
	Role     *Role   `json:"role,omitempty" validate:"omitempty,oneof=admin operator viewer auditor"`
}

// LoginRequest represents a login request
type LoginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
	TOTPCode string `json:"totp_code,omitempty"`
}

// LoginResponse represents a login response. The refresh token is delivered
// out-of-band via an HttpOnly cookie and intentionally not exposed in JSON.
type LoginResponse struct {
	AccessToken string    `json:"access_token,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	TokenType   string    `json:"token_type,omitempty"`
	User        *User     `json:"user,omitempty"`
	MFARequired bool      `json:"mfa_required,omitempty"`
}

// Permissions defines what each role can do
var Permissions = map[Role][]string{
	RoleSuperAdmin: {"*"},
	RoleAdmin:      {"users:read", "users:write", "servers:read", "servers:write", "xmpp:read", "xmpp:write", "audit:read"},
	RoleOperator:   {"servers:read", "xmpp:read", "xmpp:write"},
	RoleViewer:     {"servers:read", "xmpp:read"},
	RoleAuditor:    {"audit:read", "servers:read"},
}

// HasPermission checks if a role has a specific permission
func (r Role) HasPermission(permission string) bool {
	perms, ok := Permissions[r]
	if !ok {
		return false
	}

	for _, p := range perms {
		if p == "*" || p == permission {
			return true
		}
	}
	return false
}
