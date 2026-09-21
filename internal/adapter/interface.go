package adapter

import "context"

// Adapter is the protocol-neutral surface every backend implements in full.
// An operation the upstream cannot perform returns a *Error of Kind
// NotSupported and must be absent from Capabilities().
type Adapter interface {
	// Probe reaches the server once: liveness, credentials, version, domains
	// and the deployment facts that decide the dynamic part of Capabilities().
	Probe(ctx context.Context) (*ServerInfo, error)
	Capabilities() CapabilitySet
	Stats(ctx context.Context) (*Stats, error)
	Close() error

	ListAccounts(ctx context.Context, q ListQuery) (Page[Account], error)
	GetAccount(ctx context.Context, id string) (*Account, error)
	CreateAccount(ctx context.Context, req CreateAccount) (*Account, error)
	DeleteAccount(ctx context.Context, id string) error
	SetPassword(ctx context.Context, id, password string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	SetAdmin(ctx context.Context, id string, admin bool) error

	ListSessions(ctx context.Context, q ListQuery) (Page[Session], error)
	ListAccountSessions(ctx context.Context, accountID string) ([]Session, error)
	TerminateSession(ctx context.Context, accountID, sessionID string) error
	TerminateAccountSessions(ctx context.Context, accountID string) error

	ListRooms(ctx context.Context, q ListQuery) (Page[Room], error)
	GetRoom(ctx context.Context, id string) (*Room, error)
	CreateRoom(ctx context.Context, req CreateRoom) (*Room, error)
	DeleteRoom(ctx context.Context, id string) error
}

// MatrixAdmin is the optional moderation surface of a Matrix backend. The
// matrix handlers take it by type assertion and answer 501 without it, so
// XMPP implementations carry no stubs. Each method is guarded by one
// matrix.* capability, with the same NotSupported rule as Adapter.
type MatrixAdmin interface {
	// Deactivate with erase also removes the account's message history;
	// neither form can be undone.
	Deactivate(ctx context.Context, id string, erase bool) error
	SetSuspended(ctx context.Context, id string, suspended bool) error
	SetShadowBanned(ctx context.Context, id string, banned bool) error
	ListRegistrationTokens(ctx context.Context) ([]RegistrationToken, error)
	CreateRegistrationToken(ctx context.Context, req CreateRegistrationToken) (*RegistrationToken, error)
	DeleteRegistrationToken(ctx context.Context, token string) error
	ListReports(ctx context.Context, q ListQuery) (Page[EventReport], error)
	ListAccountMedia(ctx context.Context, id string, q ListQuery) (Page[Media], error)
	// QuarantineAccountMedia returns how many items were quarantined.
	QuarantineAccountMedia(ctx context.Context, id string) (int, error)
	DeleteMedia(ctx context.Context, mediaID string) error
	BlockRoom(ctx context.Context, roomID string, block bool) error
	// PurgeRoom starts an asynchronous shutdown and returns its task id.
	PurgeRoom(ctx context.Context, roomID string, opts PurgeRoom) (string, error)
	SendServerNotice(ctx context.Context, accountID, body string) error
	ListFederationDestinations(ctx context.Context, q ListQuery) (Page[FederationDestination], error)
}
