package matrixgeneric

import (
	"context"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// The MatrixAdmin side: only the spec suspend switch exists; everything else
// is a Synapse admin API operation and answers NotSupported.

// SetSuspended is the spec suspend: a suspended account stays logged in but
// can only read; the server refuses its sends, joins and profile changes.
func (a *Adapter) SetSuspended(ctx context.Context, id string, suspended bool) error {
	const op = "matrix.suspend"
	if err := a.declared(op, adapter.CapMatrixSuspend); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
	}
	return a.moderate(ctx, op, mxid, "suspend", "suspended", suspended)
}

func (a *Adapter) Deactivate(context.Context, string, bool) error {
	return adapter.NotSupportedError("matrix.deactivate")
}

func (a *Adapter) SetShadowBanned(context.Context, string, bool) error {
	return adapter.NotSupportedError("matrix.shadow_ban")
}

func (a *Adapter) ListRegistrationTokens(context.Context) ([]adapter.RegistrationToken, error) {
	return nil, adapter.NotSupportedError("matrix.registration_tokens.list")
}

func (a *Adapter) CreateRegistrationToken(context.Context, adapter.CreateRegistrationToken) (*adapter.RegistrationToken, error) {
	return nil, adapter.NotSupportedError("matrix.registration_tokens.create")
}

func (a *Adapter) DeleteRegistrationToken(context.Context, string) error {
	return adapter.NotSupportedError("matrix.registration_tokens.delete")
}

func (a *Adapter) ListReports(context.Context, adapter.ListQuery) (adapter.Page[adapter.EventReport], error) {
	return adapter.Page[adapter.EventReport]{}, adapter.NotSupportedError("matrix.reports")
}

func (a *Adapter) ListAccountMedia(context.Context, string, adapter.ListQuery) (adapter.Page[adapter.Media], error) {
	return adapter.Page[adapter.Media]{}, adapter.NotSupportedError("matrix.media.list")
}

func (a *Adapter) QuarantineAccountMedia(context.Context, string) (int, error) {
	return 0, adapter.NotSupportedError("matrix.media.quarantine")
}

func (a *Adapter) DeleteMedia(context.Context, string) error {
	return adapter.NotSupportedError("matrix.media.delete")
}

func (a *Adapter) BlockRoom(context.Context, string, bool) error {
	return adapter.NotSupportedError("matrix.room_block")
}

func (a *Adapter) PurgeRoom(context.Context, string, adapter.PurgeRoom) (string, error) {
	return "", adapter.NotSupportedError("matrix.room_purge")
}

func (a *Adapter) SendServerNotice(context.Context, string, string) error {
	return adapter.NotSupportedError("matrix.server_notice")
}

func (a *Adapter) ListFederationDestinations(context.Context, adapter.ListQuery) (adapter.Page[adapter.FederationDestination], error) {
	return adapter.Page[adapter.FederationDestination]{}, adapter.NotSupportedError("matrix.federation")
}
