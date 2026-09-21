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
