package adapter

import (
	"encoding/json"
	"sort"
)

// Capability names an operation the UI may offer. Declaring one the adapter
// then answers NotSupported for is a defect the contract tests catch.
type Capability string

const (
	CapAccountsList        Capability = "accounts.list"
	CapAccountsGet         Capability = "accounts.get"    // single lookup; a backend may have it without a listing
	CapAccountsSearch      Capability = "accounts.search" // server-side search; otherwise the UI filters locally
	CapAccountsCreate      Capability = "accounts.create"
	CapAccountsDelete      Capability = "accounts.delete"
	CapAccountsSetPassword Capability = "accounts.set_password"
	CapAccountsSetEnabled  Capability = "accounts.set_enabled"
	CapAccountsSetAdmin    Capability = "accounts.set_admin"
	CapSessionsListAll     Capability = "sessions.list_all"
	CapSessionsListByAcct  Capability = "sessions.list_by_account"
	CapSessionsTerminate   Capability = "sessions.terminate"
	CapRoomsList           Capability = "rooms.list"
	CapRoomsGet            Capability = "rooms.get"
	CapRoomsCreate         Capability = "rooms.create"
	CapRoomsDelete         Capability = "rooms.delete"

	// MatrixAdmin operations. Locking is not here: it is accounts.set_enabled.
	CapMatrixDeactivate      Capability = "matrix.deactivate"
	CapMatrixSuspend         Capability = "matrix.suspend"
	CapMatrixShadowBan       Capability = "matrix.shadow_ban"
	CapMatrixRegTokens       Capability = "matrix.registration_tokens"
	CapMatrixReports         Capability = "matrix.reports"
	CapMatrixMedia           Capability = "matrix.media" // list and delete; quarantine is its own bit
	CapMatrixMediaQuarantine Capability = "matrix.media_quarantine"
	CapMatrixRoomBlock       Capability = "matrix.room_block"
	CapMatrixRoomPurge       Capability = "matrix.room_purge"
	CapMatrixServerNotice    Capability = "matrix.server_notice"
	CapMatrixFederation      Capability = "matrix.federation"
)

// AllCapabilities lists every declared capability; the frontend copy is kept
// in step with it by a test.
var AllCapabilities = []Capability{
	CapAccountsList, CapAccountsGet, CapAccountsSearch, CapAccountsCreate, CapAccountsDelete,
	CapAccountsSetPassword, CapAccountsSetEnabled, CapAccountsSetAdmin,
	CapSessionsListAll, CapSessionsListByAcct, CapSessionsTerminate,
	CapRoomsList, CapRoomsGet, CapRoomsCreate, CapRoomsDelete,
	CapMatrixDeactivate, CapMatrixSuspend, CapMatrixShadowBan, CapMatrixRegTokens, CapMatrixReports,
	CapMatrixMedia, CapMatrixMediaQuarantine, CapMatrixRoomBlock, CapMatrixRoomPurge, CapMatrixServerNotice, CapMatrixFederation,
}

// MatrixCapabilities are the ones MatrixAdmin serves.
var MatrixCapabilities = []Capability{
	CapMatrixDeactivate, CapMatrixSuspend, CapMatrixShadowBan, CapMatrixRegTokens, CapMatrixReports,
	CapMatrixMedia, CapMatrixMediaQuarantine, CapMatrixRoomBlock, CapMatrixRoomPurge, CapMatrixServerNotice, CapMatrixFederation,
}

type CapabilitySet map[Capability]struct{}

func NewCapabilitySet(caps ...Capability) CapabilitySet {
	s := make(CapabilitySet, len(caps))
	for _, c := range caps {
		s[c] = struct{}{}
	}
	return s
}

func (s CapabilitySet) Has(c Capability) bool {
	_, ok := s[c]
	return ok
}

// Without returns a copy lacking the given capabilities.
func (s CapabilitySet) Without(caps ...Capability) CapabilitySet {
	out := make(CapabilitySet, len(s))
	for c := range s {
		out[c] = struct{}{}
	}
	for _, c := range caps {
		delete(out, c)
	}
	return out
}

func (s CapabilitySet) Sorted() []Capability {
	out := make([]Capability, 0, len(s))
	for c := range s {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s CapabilitySet) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.Sorted())
}

func (s *CapabilitySet) UnmarshalJSON(data []byte) error {
	var list []Capability
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*s = NewCapabilitySet(list...)
	return nil
}
