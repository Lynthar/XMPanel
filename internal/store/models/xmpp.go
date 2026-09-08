package models

import (
	"time"
)

// ServerType represents the type of XMPP server
type ServerType string

const (
	ServerTypeProsody  ServerType = "prosody"
	ServerTypeEjabberd ServerType = "ejabberd"
)

// XMPPServer represents an XMPP server configuration
type XMPPServer struct {
	ID              int64      `json:"id" db:"id"`
	Name            string     `json:"name" db:"name"`
	Type            ServerType `json:"type" db:"type"`
	Host            string     `json:"host" db:"host"`
	Port            int        `json:"port" db:"port"`
	APIKeyEncrypted string     `json:"-" db:"api_key_encrypted"`
	TLSEnabled      bool       `json:"tls_enabled" db:"tls_enabled"`
	Enabled         bool       `json:"enabled" db:"enabled"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" db:"updated_at"`
}

// CreateXMPPServerRequest represents a request to add an XMPP server
type CreateXMPPServerRequest struct {
	Name       string     `json:"name"`
	Type       ServerType `json:"type"`
	Host       string     `json:"host"`
	Port       int        `json:"port"`
	APIKey     string     `json:"api_key"`
	TLSEnabled bool       `json:"tls_enabled"`
}

// UpdateXMPPServerRequest represents a request to update an XMPP server
type UpdateXMPPServerRequest struct {
	Name       *string `json:"name,omitempty"`
	APIKey     *string `json:"api_key,omitempty"`
	TLSEnabled *bool   `json:"tls_enabled,omitempty"`
	Enabled    *bool   `json:"enabled,omitempty"`
}

// XMPPUser represents a user on an XMPP server
type XMPPUser struct {
	JID       string    `json:"jid"`
	Username  string    `json:"username"`
	Domain    string    `json:"domain"`
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Resources []string  `json:"resources,omitempty"`
}

// CreateXMPPUserRequest represents a request to create an XMPP user
type CreateXMPPUserRequest struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Password string `json:"password"`
}

// XMPPSession represents an active XMPP session
type XMPPSession struct {
	JID       string    `json:"jid"`
	Resource  string    `json:"resource"`
	IPAddress string    `json:"ip_address"`
	Priority  int       `json:"priority"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
}

// XMPPRoom represents a MUC room
type XMPPRoom struct {
	JID         string    `json:"jid"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Occupants   int       `json:"occupants"`
	Public      bool      `json:"public"`
	Persistent  bool      `json:"persistent"`
	MembersOnly bool      `json:"members_only"`
	Moderated   bool      `json:"moderated"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// CreateXMPPRoomRequest represents a request to create a MUC room
type CreateXMPPRoomRequest struct {
	Name        string `json:"name"`
	Domain      string `json:"domain"`
	Description string `json:"description,omitempty"`
	Public      bool   `json:"public"`
	Persistent  bool   `json:"persistent"`
	MembersOnly bool   `json:"members_only"`
}

// ServerStats represents XMPP server statistics
type ServerStats struct {
	OnlineUsers     int                    `json:"online_users"`
	RegisteredUsers int                    `json:"registered_users"`
	ActiveSessions  int                    `json:"active_sessions"`
	S2SConnections  int                    `json:"s2s_connections"`
	Uptime          int64                  `json:"uptime_seconds"`
	Version         string                 `json:"version"`
	Modules         []string               `json:"modules,omitempty"`
	Extra           map[string]interface{} `json:"extra,omitempty"`
}
