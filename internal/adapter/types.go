package adapter

import (
	"strings"
	"time"
)

type Protocol string

const (
	ProtocolXMPP   Protocol = "xmpp"
	ProtocolMatrix Protocol = "matrix"
)

type Implementation string

const (
	ImplProsody  Implementation = "prosody"
	ImplEjabberd Implementation = "ejabberd"
	ImplSynapse  Implementation = "synapse"
	ImplTuwunel  Implementation = "tuwunel" // served by the synapse package behind a static mask
)

// ServerConfig is what the registry hands to a constructor; credentials are
// already decrypted.
type ServerConfig struct {
	ID       int64
	Protocol Protocol
	Impl     Implementation
	Endpoint string // admin API base URL the panel connects to
	Domain   string // protocol identity: XMPP VirtualHost or Matrix server_name
	Creds    Credentials
}

// Credentials is the decrypted form of servers.credentials_encrypted. Kind
// "bearer" needs Token only; "bearer+mas" adds the MAS admin client that a
// Synapse delegating authentication needs for account lifecycle operations.
type Credentials struct {
	Kind  string          `json:"kind"`
	Token string          `json:"token,omitempty"`
	MAS   *MASCredentials `json:"mas,omitempty"`
}

// MASCredentials identify the panel to Matrix Authentication Service's admin
// API: an OAuth 2.0 client declared with client_secret_basic and listed in
// policy.data.admin_clients.
type MASCredentials struct {
	Endpoint     string `json:"endpoint"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

const (
	CredentialsBearer    = "bearer"
	CredentialsBearerMAS = "bearer+mas"
)

type Account struct {
	ID          string              `json:"id"` // bare JID or MXID
	Localpart   string              `json:"localpart"`
	Domain      string              `json:"domain"`
	DisplayName string              `json:"display_name,omitempty"`
	Enabled     bool                `json:"enabled"`
	Admin       bool                `json:"admin"`
	CreatedAt   *time.Time          `json:"created_at,omitempty"`
	LastSeen    *time.Time          `json:"last_seen,omitempty"`
	Matrix      *MatrixAccountFacts `json:"matrix,omitempty"`
	XMPP        *XMPPAccountFacts   `json:"xmpp,omitempty"`
}

type XMPPAccountFacts struct {
	Roles []string `json:"roles,omitempty"`
}

// MatrixAccountFacts are the states Synapse keeps beside Enabled, which is
// !deactivated && !locked. Suspended is nil when unknown: the account
// listing does not carry it, only the single-account query does.
type MatrixAccountFacts struct {
	Deactivated  bool   `json:"deactivated"`
	Locked       bool   `json:"locked"`
	Suspended    *bool  `json:"suspended,omitempty"`
	ShadowBanned bool   `json:"shadow_banned"`
	Erased       bool   `json:"erased"`
	UserType     string `json:"user_type,omitempty"`
}

type CreateAccount struct {
	Localpart   string `json:"localpart"`
	Domain      string `json:"domain,omitempty"` // empty = the server's own domain
	Password    string `json:"password"`
	DisplayName string `json:"display_name,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
}

// Session is a live XMPP connection or a persistent Matrix device.
type Session struct {
	ID        string            `json:"id"` // XMPP full JID or Matrix device_id
	AccountID string            `json:"account_id"`
	Name      string            `json:"name,omitempty"` // resource or device display name
	IP        string            `json:"ip,omitempty"`
	UserAgent string            `json:"user_agent,omitempty"`
	StartedAt *time.Time        `json:"started_at,omitempty"`
	LastSeen  *time.Time        `json:"last_seen,omitempty"`
	Live      bool              `json:"live"`
	XMPP      *XMPPSessionFacts `json:"xmpp,omitempty"`
}

type XMPPSessionFacts struct {
	Priority int    `json:"priority"`
	Status   string `json:"status,omitempty"`
}

type Room struct {
	ID      string           `json:"id"` // room JID, or !opaque:server (no server part from room version 12)
	Name    string           `json:"name,omitempty"`
	Alias   string           `json:"alias,omitempty"`
	Members int              `json:"members"`
	Public  bool             `json:"public"`
	Matrix  *MatrixRoomFacts `json:"matrix,omitempty"`
	XMPP    *XMPPRoomFacts   `json:"xmpp,omitempty"`
}

type MatrixRoomFacts struct {
	Version            string `json:"version"`
	Creator            string `json:"creator,omitempty"`
	Encryption         string `json:"encryption,omitempty"`
	Federatable        bool   `json:"federatable"`
	JoinedLocalMembers int    `json:"joined_local_members"`
	Topic              string `json:"topic,omitempty"`
}

type XMPPRoomFacts struct {
	Description string `json:"description,omitempty"`
	Persistent  bool   `json:"persistent"`
	MembersOnly bool   `json:"members_only"`
	Moderated   bool   `json:"moderated"`
}

type CreateRoom struct {
	Name        string `json:"name"`
	Domain      string `json:"domain,omitempty"` // MUC service; empty = the implementation's default
	Description string `json:"description,omitempty"`
	Public      bool   `json:"public"`
	Persistent  bool   `json:"persistent"`
	MembersOnly bool   `json:"members_only"`
}

// RegistrationToken is one token that lets a user register. UsesAllowed nil
// means unlimited; Pending counts registrations in progress, which only
// Synapse reports; Revoked is a MAS state that keeps the row visible.
type RegistrationToken struct {
	Token       string     `json:"token"`
	UsesAllowed *int       `json:"uses_allowed"`
	Used        int        `json:"used"`
	Pending     int        `json:"pending"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	Valid       bool       `json:"valid"`
	Revoked     bool       `json:"revoked"`
}

type CreateRegistrationToken struct {
	Token       string     `json:"token,omitempty"` // empty = generated upstream
	UsesAllowed *int       `json:"uses_allowed,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// MaskToken keeps the first characters of a registration token so a log or
// audit row identifies it without disclosing what a stranger could register
// with; very short tokens are hidden entirely.
func MaskToken(token string) string {
	const keep = 4
	if len(token) <= keep {
		return strings.Repeat("*", len(token))
	}
	return token[:keep] + strings.Repeat("*", len(token)-keep)
}

// EventReport is a user's report of an event, as moderation sees it.
type EventReport struct {
	ID         string    `json:"id"`
	ReceivedAt time.Time `json:"received_at"`
	RoomID     string    `json:"room_id"`
	RoomName   string    `json:"room_name,omitempty"`
	RoomAlias  string    `json:"room_alias,omitempty"`
	EventID    string    `json:"event_id"`
	Reporter   string    `json:"reporter"`
	Sender     string    `json:"sender"`
	Reason     string    `json:"reason,omitempty"`
	Score      int       `json:"score"`
}

// Media is one item in the local media repository.
type Media struct {
	ID          string     `json:"id"`
	Type        string     `json:"type,omitempty"`
	Size        int64      `json:"size"`
	Name        string     `json:"name,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	LastAccess  *time.Time `json:"last_access,omitempty"`
	Quarantined bool       `json:"quarantined"`
	Protected   bool       `json:"protected"`
}

// PurgeRoom carries the shutdown options: members are kicked and, with
// NewRoomUser set, moved to a replacement room carrying Message.
type PurgeRoom struct {
	Block       bool   `json:"block"`
	Purge       bool   `json:"purge"`
	ForcePurge  bool   `json:"force_purge"`
	NewRoomUser string `json:"new_room_user,omitempty"`
	NewRoomName string `json:"new_room_name,omitempty"`
	Message     string `json:"message,omitempty"`
}

// FederationDestination is a remote server this one has tried to reach.
// Healthy means the last attempt succeeded and no backoff is in progress.
type FederationDestination struct {
	Destination     string     `json:"destination"`
	Healthy         bool       `json:"healthy"`
	LastFailureAt   *time.Time `json:"last_failure_at,omitempty"`
	FailingSince    *time.Time `json:"failing_since,omitempty"`
	RetryIntervalMS int64      `json:"retry_interval_ms"`
}

// Stats counters are pointers: nil means the backend cannot report that
// figure, and the UI shows a dash instead of a misleading zero.
type Stats struct {
	Version         string `json:"version"`
	UptimeSeconds   *int64 `json:"uptime_seconds"`
	RegisteredUsers *int   `json:"registered_users"`
	OnlineUsers     *int   `json:"online_users"`
	ActiveSessions  *int   `json:"active_sessions"`
	Rooms           *int   `json:"rooms"`
	S2SConnections  *int   `json:"s2s_connections"`
}

type ServerInfo struct {
	Protocol Protocol       `json:"protocol"`
	Impl     Implementation `json:"implementation"`
	Version  string         `json:"version"`
	Domains  []string       `json:"domains"`
	AuthMode string         `json:"auth_mode,omitempty"`
}

// ListQuery is a page request. Cursor is opaque to callers; "" is the first
// page. Handlers clamp Limit before the query reaches an adapter.
type ListQuery struct {
	Search string
	Domain string
	Limit  int
	Cursor string
}

const (
	DefaultLimit = 100
	MaxLimit     = 500
)

type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
	Total *int   `json:"total,omitempty"`
}
