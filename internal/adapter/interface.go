package adapter

import (
	"context"

	"github.com/xmpanel/xmpanel/internal/store/models"
	apperrors "github.com/xmpanel/xmpanel/pkg/errors"
	"github.com/xmpanel/xmpanel/pkg/types"
)

// Re-export errors for convenience
var (
	ErrNotImplemented   = apperrors.ErrNotImplemented
	ErrConnectionFailed = apperrors.ErrConnectionFailed
	ErrAuthFailed       = apperrors.ErrAuthFailed
	ErrUserNotFound     = apperrors.ErrUserNotFound
	ErrUserExists       = apperrors.ErrUserExists
	ErrRoomNotFound     = apperrors.ErrRoomNotFound
	ErrRoomExists       = apperrors.ErrRoomExists
	ErrOperationFailed  = apperrors.ErrOperationFailed
)

// Re-export types for convenience
type (
	ServerInfo = types.ServerInfo
	ModuleInfo = types.ModuleInfo
)

// Capabilities advertises what an adapter can actually do against the
// underlying server. Different XMPP servers (and different versions of the
// same server) expose different admin endpoints — the panel uses these
// flags to hide UI elements and stat tiles that would otherwise return 502.
type Capabilities struct {
	// Stats counters available via GetStats. Booleans rather than per-field
	// flags so callers don't need to introspect the struct.
	OnlineUsersCount    bool `json:"online_users_count"`
	RegisteredUsersCount bool `json:"registered_users_count"`
	ActiveSessionsCount bool `json:"active_sessions_count"`
	S2SConnectionsCount bool `json:"s2s_connections_count"`

	// Live session listing & disconnection (GetOnlineSessions / KickSession / KickUser).
	Sessions bool `json:"sessions"`

	// MUC room listing & management.
	Rooms bool `json:"rooms"`

	// Module enable/disable.
	Modules bool `json:"modules"`
}

// XMPPAdapter defines the interface for XMPP server adapters
// Both Prosody and ejabberd adapters implement this interface
type XMPPAdapter interface {
	// Connection
	Connect(ctx context.Context) error
	Disconnect() error
	Ping(ctx context.Context) error

	// Server info
	GetServerInfo(ctx context.Context) (*types.ServerInfo, error)
	GetStats(ctx context.Context) (*models.ServerStats, error)

	// User management
	ListUsers(ctx context.Context, domain string) ([]models.XMPPUser, error)
	GetUser(ctx context.Context, username, domain string) (*models.XMPPUser, error)
	CreateUser(ctx context.Context, req models.CreateXMPPUserRequest) error
	DeleteUser(ctx context.Context, username, domain string) error
	ChangePassword(ctx context.Context, username, domain, newPassword string) error

	// Session management
	GetOnlineSessions(ctx context.Context) ([]models.XMPPSession, error)
	GetUserSessions(ctx context.Context, username, domain string) ([]models.XMPPSession, error)
	KickSession(ctx context.Context, jid string) error
	KickUser(ctx context.Context, username, domain string) error

	// MUC (Multi-User Chat) management
	ListRooms(ctx context.Context, mucDomain string) ([]models.XMPPRoom, error)
	GetRoom(ctx context.Context, room, mucDomain string) (*models.XMPPRoom, error)
	CreateRoom(ctx context.Context, req models.CreateXMPPRoomRequest) error
	DeleteRoom(ctx context.Context, room, mucDomain string) error

	// Module management (if supported)
	ListModules(ctx context.Context) ([]types.ModuleInfo, error)
	EnableModule(ctx context.Context, module string) error
	DisableModule(ctx context.Context, module string) error

	// Capabilities returns which features this adapter supports against
	// the configured server. Cheap (no network call); the handler layer
	// caches it per-server-record on the frontend.
	Capabilities() Capabilities
}

