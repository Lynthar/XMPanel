package adapter

import "time"

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
	ID      string           `json:"id"` // room JID or !id:server
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
