package synapse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Adapter drives Synapse through its admin API with one admin access token.
// Under MAS delegation the account lifecycle goes through MAS's admin API when
// the credentials carry a MAS client, and is undeclared when they do not.
// Tuwunel serves the same API and is driven by the same code behind a mask.
type Adapter struct {
	cfg        adapter.ServerConfig
	httpClient *http.Client
	baseURL    string
	mas        *masClient
	mu         sync.RWMutex
	authMode   string
	passwords  bool // MAS accepts password logins, so creating and setting passwords work
	caps       adapter.CapabilitySet
}

const (
	AuthModeLegacy = "legacy"
	AuthModeMAS    = "mas"
)

// legacyCapabilities is the full set: the protocol-neutral operations plus
// every MatrixAdmin one. matrix.server_notice cannot be probed (Synapse only
// reveals a missing server_notices block when a notice is sent) and answers
// NotSupported then; it is the one declared capability that may do so.
var legacyCapabilities = adapter.NewCapabilitySet(append([]adapter.Capability{
	adapter.CapAccountsList, adapter.CapAccountsSearch, adapter.CapAccountsCreate, adapter.CapAccountsDelete,
	adapter.CapAccountsSetPassword, adapter.CapAccountsSetEnabled, adapter.CapAccountsSetAdmin,
	adapter.CapSessionsListByAcct, adapter.CapSessionsTerminate,
	adapter.CapRoomsList, adapter.CapRoomsGet, adapter.CapRoomsDelete,
}, adapter.MatrixCapabilities...)...)

// lifecycle names the operations MAS owns once authentication is delegated.
var lifecycle = append([]adapter.Capability{
	adapter.CapAccountsCreate, adapter.CapAccountsDelete, adapter.CapAccountsSetPassword,
	adapter.CapAccountsSetEnabled, adapter.CapAccountsSetAdmin,
}, masLifecycle...)

// tuwunelMask is what Tuwunel 1.9 leaves out of the Synapse admin API: those
// routes answer 404 M_UNRECOGNIZED, so they are never declared for it.
var tuwunelMask = []adapter.Capability{adapter.CapMatrixShadowBan, adapter.CapMatrixReports, adapter.CapMatrixMediaQuarantine}

func New(cfg adapter.ServerConfig) *Adapter {
	a := &Adapter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		baseURL:   strings.TrimRight(cfg.Endpoint, "/"),
		authMode:  AuthModeLegacy,
		passwords: true,
	}
	a.caps = a.static()
	if cfg.Creds.MAS != nil {
		a.mas = newMASClient(cfg.Creds.MAS, a.httpClient)
	}
	return a
}

// static is the capability set before the probe narrows it.
func (a *Adapter) static() adapter.CapabilitySet {
	if a.cfg.Impl == adapter.ImplTuwunel {
		return legacyCapabilities.Without(tuwunelMask...)
	}
	return legacyCapabilities
}

// Probe checks reachability, the token, the delegation mode, the version and
// admin rights in that order, so the first failure names the layer at fault.
func (a *Adapter) Probe(ctx context.Context) (*adapter.ServerInfo, error) {
	const op = "server.probe"
	var versions struct {
		Versions []string `json:"versions"`
	}
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_matrix/client/versions", anonymous: true}, &versions); err != nil {
		return nil, err
	}
	if len(versions.Versions) == 0 {
		return nil, &adapter.Error{Kind: adapter.Upstream, Op: op, Status: http.StatusOK, Err: errors.New("endpoint is not a Matrix homeserver")}
	}
	var who struct {
		UserID string `json:"user_id"`
	}
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_matrix/client/v3/account/whoami"}, &who); err != nil {
		return nil, err
	}
	_, serverName := adapter.SplitMXID(who.UserID)
	if serverName == "" {
		return nil, &adapter.Error{Kind: adapter.Upstream, Op: op, Status: http.StatusOK, Err: fmt.Errorf("whoami returned no user id: %q", who.UserID)}
	}
	// Every id the adapter composes ends with the configured domain, so a
	// mismatch would make every account operation fail as "not local".
	if serverName != a.cfg.Domain {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: who.UserID, Err: fmt.Errorf("server_name is %q, not %q", serverName, a.cfg.Domain)}
	}

	authMode, err := a.detectAuthMode(ctx, op)
	if err != nil {
		return nil, err
	}
	version, err := a.serverVersion(ctx, op)
	if err != nil {
		return nil, err
	}
	// server_version needs no token on either implementation, so admin rights
	// are proven on the admin's own record: a non-admin token fails here as
	// Forbidden instead of as 502 on every later request.
	if _, err := a.call(ctx, request{op: op, resource: who.UserID, method: http.MethodGet, path: userPath(who.UserID)}, nil); err != nil {
		return nil, err
	}
	passwords := true
	if authMode == AuthModeMAS && a.mas != nil {
		if passwords, err = a.masPasswordLogin(ctx, op); err != nil {
			return nil, err
		}
	}
	caps := a.static()
	switch {
	case authMode == AuthModeMAS && a.mas == nil:
		caps = caps.Without(lifecycle...)
	case authMode == AuthModeMAS && !passwords:
		caps = caps.Without(adapter.CapAccountsCreate, adapter.CapAccountsSetPassword)
	}
	if a.cfg.Impl == adapter.ImplTuwunel {
		tokens, err := a.tuwunelServesTokens(ctx, op)
		if err != nil {
			return nil, err
		}
		if !tokens {
			caps = caps.Without(adapter.CapMatrixRegTokens)
		}
	}
	a.mu.Lock()
	a.authMode, a.passwords, a.caps = authMode, passwords, caps
	a.mu.Unlock()
	return &adapter.ServerInfo{
		Protocol: adapter.ProtocolMatrix,
		Impl:     a.cfg.Impl,
		Version:  version,
		Domains:  []string{serverName},
		AuthMode: authMode,
	}, nil
}

// detectAuthMode reads whether authentication is delegated to MAS. Tuwunel
// never is: its own OIDC server answers auth_metadata too and it takes no
// MAS tokens, so MAS credentials are refused rather than probed.
func (a *Adapter) detectAuthMode(ctx context.Context, op string) (string, error) {
	if a.cfg.Impl == adapter.ImplTuwunel {
		if a.mas != nil {
			return "", &adapter.Error{Kind: adapter.Invalid, Op: op, Err: errors.New("MAS credentials were given but Tuwunel does not accept MAS tokens")}
		}
		return AuthModeLegacy, nil
	}
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_matrix/client/v1/auth_metadata", anonymous: true}, nil); err != nil {
		if failure, ok := adapter.AsError(err); !ok || failure.Status != http.StatusNotFound {
			return "", err
		}
		if a.mas != nil {
			return "", &adapter.Error{Kind: adapter.Invalid, Op: op, Err: errors.New("MAS credentials were given but the homeserver does not delegate authentication")}
		}
		return AuthModeLegacy, nil
	}
	return AuthModeMAS, nil
}

// masPasswordLogin verifies the MAS credentials against MAS itself and reads
// whether it takes password logins; an upstream identity provider cannot
// create accounts with a password or set one.
func (a *Adapter) masPasswordLogin(ctx context.Context, op string) (bool, error) {
	var site struct {
		ServerName    string `json:"server_name"`
		PasswordLogin bool   `json:"password_login_enabled"`
	}
	if _, err := a.mas.call(ctx, request{op: op, method: http.MethodGet, path: "/api/admin/v1/site-config"}, &site); err != nil {
		return false, err
	}
	if site.ServerName != a.cfg.Domain {
		return false, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: site.ServerName, Err: fmt.Errorf("MAS serves server_name %q, not %q", site.ServerName, a.cfg.Domain)}
	}
	return site.PasswordLogin, nil
}

// tuwunelServesTokens reports whether the registration token routes exist:
// Tuwunel drops them once MAS provisioning (mas_secret) is configured, and
// nothing else reveals that.
func (a *Adapter) tuwunelServesTokens(ctx context.Context, op string) (bool, error) {
	_, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_synapse/admin/v1/registration_tokens"}, nil)
	if failure, ok := adapter.AsError(err); ok && failure.Kind == adapter.NotSupported {
		return false, nil
	}
	return err == nil, err
}

func (a *Adapter) Capabilities() adapter.CapabilitySet {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.caps
}

func (a *Adapter) Close() error {
	a.httpClient.CloseIdleConnections()
	return nil
}

// Stats takes the totals off one-item listings; a listing that fails leaves
// its counter nil rather than reported as zero.
func (a *Adapter) Stats(ctx context.Context) (*adapter.Stats, error) {
	const op = "server.stats"
	version, err := a.serverVersion(ctx, op)
	if err != nil {
		return nil, err
	}
	stats := &adapter.Stats{Version: version}
	if page, err := a.listUsers(ctx, op, adapter.ListQuery{Limit: 1}); err == nil {
		total := page.Total
		stats.RegisteredUsers = &total
	}
	if page, err := a.listRooms(ctx, op, adapter.ListQuery{Limit: 1}); err == nil {
		total := page.TotalRooms
		stats.Rooms = &total
	}
	return stats, nil
}

func (a *Adapter) serverVersion(ctx context.Context, op string) (string, error) {
	var version struct {
		ServerVersion string `json:"server_version"`
	}
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_synapse/admin/v1/server_version"}, &version); err != nil {
		return "", err
	}
	return version.ServerVersion, nil
}

func (a *Adapter) ListAccounts(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Account], error) {
	page, err := a.listUsers(ctx, "accounts.list", q)
	if err != nil {
		return adapter.Page[adapter.Account]{}, err
	}
	accounts := make([]adapter.Account, len(page.Users))
	for i, u := range page.Users {
		accounts[i] = u.account()
	}
	// Under delegation the admin bit lives in MAS and is never written to
	// Synapse's column, so the listing is corrected from MAS's admin list.
	if a.delegated() {
		admins, err := a.masAdmins(ctx, "accounts.list")
		if err != nil {
			return adapter.Page[adapter.Account]{}, err
		}
		for i := range accounts {
			accounts[i].Admin = admins[accounts[i].Localpart]
		}
	}
	total := page.Total
	return adapter.Page[adapter.Account]{Items: accounts, Next: string(page.NextToken), Total: &total}, nil
}

// listUsers sends guests=false (MAS deployments require it), deactivated=false
// (the panel's delete is deactivation) and locked=true, without which Synapse
// drops locked accounts from the listing and a disabled account would vanish.
func (a *Adapter) listUsers(ctx context.Context, op string, q adapter.ListQuery) (userPage, error) {
	from, err := offset(op, q.Cursor)
	if err != nil {
		return userPage{}, err
	}
	query := url.Values{
		"from":        {strconv.Itoa(from)},
		"limit":       {strconv.Itoa(limitOf(q))},
		"guests":      {"false"},
		"deactivated": {"false"},
		"locked":      {"true"},
	}
	if q.Search != "" {
		query.Set("name", q.Search)
	}
	var page userPage
	_, err = a.call(ctx, request{op: op, method: http.MethodGet, path: "/_synapse/admin/v3/users", query: query}, &page)
	return page, err
}

func (a *Adapter) GetAccount(ctx context.Context, id string) (*adapter.Account, error) {
	const op = "accounts.get"
	mxid, err := a.mxid(op, id)
	if err != nil {
		return nil, err
	}
	u, err := a.user(ctx, op, mxid)
	if err != nil {
		return nil, err
	}
	acc := u.account()
	// Under delegation the admin bit lives in MAS; Synapse's column is stale.
	if a.delegated() {
		record, err := a.masUser(ctx, op, mxid)
		if err != nil {
			return nil, err
		}
		acc.Admin = record.Attributes.Admin
	}
	return &acc, nil
}

// user fetches one account and reports a deactivated one as missing: Matrix
// never deletes an account, so deactivation is what the panel's delete did.
func (a *Adapter) user(ctx context.Context, op, mxid string) (*user, error) {
	var u user
	if _, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodGet, path: userPath(mxid)}, &u); err != nil {
		return nil, err
	}
	if u.Deactivated {
		return nil, &adapter.Error{Kind: adapter.NotFound, Op: op, Resource: mxid, Status: http.StatusOK, Err: errors.New("account is deactivated")}
	}
	return &u, nil
}

func (a *Adapter) CreateAccount(ctx context.Context, req adapter.CreateAccount) (*adapter.Account, error) {
	const op = "accounts.create"
	viaMAS, err := a.lifecycle(op)
	if err != nil {
		return nil, err
	}
	if req.Domain != "" && req.Domain != a.cfg.Domain {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: req.Domain, Err: fmt.Errorf("server only serves %s", a.cfg.Domain)}
	}
	if req.Localpart == "" || strings.ContainsAny(req.Localpart, "@:") {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: req.Localpart, Err: errors.New("localpart is empty or contains @ or a colon")}
	}
	mxid := "@" + req.Localpart + ":" + a.cfg.Domain
	if viaMAS {
		return a.masCreateAccount(ctx, op, mxid, req)
	}
	// PUT creates or modifies, so a taken id must be detected first: a
	// deactivated account keeps its id forever and must not be revived here.
	var existing user
	_, err = a.call(ctx, request{op: op, resource: mxid, method: http.MethodGet, path: userPath(mxid)}, &existing)
	if err == nil {
		return nil, conflict(op, mxid)
	}
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.NotFound {
		return nil, err
	}
	// admin is only sent when wanted: a new account is not an admin by
	// default, and Tuwunel treats an explicit false as a revocation, which
	// fails for an account that never was one.
	body := map[string]any{"password": req.Password}
	if req.Admin {
		body["admin"] = true
	}
	if req.DisplayName != "" {
		body["displayname"] = req.DisplayName
	}
	var created user
	status, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodPut, path: userPath(mxid), body: body}, &created)
	if err != nil {
		return nil, err
	}
	// Synapse answers 201 for a creation and 200 for a modification, which
	// catches a concurrent creation; Tuwunel answers 200 for both.
	if status != http.StatusCreated && a.cfg.Impl != adapter.ImplTuwunel {
		return nil, conflict(op, mxid)
	}
	acc := created.account()
	return &acc, nil
}

// DeleteAccount deactivates without erasing; Matrix has no deletion, and the
// erasing form is a MatrixAdmin operation behind its own permission.
func (a *Adapter) DeleteAccount(ctx context.Context, id string) error {
	const op = "accounts.delete"
	viaMAS, mxid, err := a.lifecycleTarget(op, id)
	if err != nil {
		return err
	}
	if viaMAS {
		return a.masAction(ctx, op, mxid, "/deactivate", map[string]bool{"skip_erase": true})
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	_, err = a.call(ctx, request{
		op: op, resource: mxid, method: http.MethodPost,
		path: "/_synapse/admin/v1/deactivate/" + url.PathEscape(mxid),
		body: map[string]bool{"erase": false},
	}, nil)
	return err
}

// SetPassword logs every device out on both paths, as Synapse does by default
// and as the panel's own password change does; MAS has no such switch, so
// the devices are deleted afterwards. MAS also enforces its password policy
// here (a weak one is Invalid).
func (a *Adapter) SetPassword(ctx context.Context, id, password string) error {
	const op = "accounts.set_password"
	viaMAS, mxid, err := a.lifecycleTarget(op, id)
	if err != nil {
		return err
	}
	if viaMAS {
		if err := a.masAction(ctx, op, mxid, "/set-password", map[string]any{"password": password}); err != nil {
			return err
		}
		return a.TerminateAccountSessions(ctx, mxid)
	}
	return a.modify(ctx, op, mxid, map[string]any{"password": password, "logout_devices": true})
}

// SetEnabled maps to the lock: a locked account cannot log in and its clients
// are soft-logged-out; unlocking restores it unchanged. MAS syncs its lock
// into Synapse.
func (a *Adapter) SetEnabled(ctx context.Context, id string, enabled bool) error {
	const op = "accounts.set_enabled"
	viaMAS, mxid, err := a.lifecycleTarget(op, id)
	if err != nil {
		return err
	}
	if viaMAS {
		action := "/lock"
		if enabled {
			action = "/unlock"
		}
		return a.masAction(ctx, op, mxid, action, nil)
	}
	return a.modify(ctx, op, mxid, map[string]any{"locked": !enabled})
}

// modify applies fields through the create-or-modify endpoint after checking
// the account exists, because the same PUT would otherwise create it.
func (a *Adapter) modify(ctx context.Context, op, mxid string, fields map[string]any) error {
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	_, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodPut, path: userPath(mxid), body: fields}, nil)
	return err
}

// SetAdmin through MAS sets can_request_admin, which is what grants the
// urn:synapse:admin scope; Synapse's own admin column is not consulted then.
func (a *Adapter) SetAdmin(ctx context.Context, id string, admin bool) error {
	const op = "accounts.set_admin"
	viaMAS, mxid, err := a.lifecycleTarget(op, id)
	if err != nil {
		return err
	}
	if viaMAS {
		return a.masAction(ctx, op, mxid, "/set-admin", map[string]bool{"admin": admin})
	}
	if a.cfg.Impl == adapter.ImplTuwunel {
		return a.tuwunelSetAdmin(ctx, op, mxid, admin)
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	_, err = a.call(ctx, request{
		op: op, resource: mxid, method: http.MethodPut,
		path: "/_synapse/admin/v1/users/" + url.PathEscape(mxid) + "/admin",
		body: map[string]bool{"admin": admin},
	}, nil)
	return err
}

// tuwunelSetAdmin uses the create-or-modify PUT, the only admin-bit route
// Tuwunel serves. The grant joins the admin room: with no such room the PUT
// changes nothing (so the reply is checked), and revoking a non-admin errors.
func (a *Adapter) tuwunelSetAdmin(ctx context.Context, op, mxid string, admin bool) error {
	current, err := a.user(ctx, op, mxid)
	if err != nil {
		return err
	}
	if bool(current.Admin) == admin {
		return nil
	}
	var updated user
	if _, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodPut, path: userPath(mxid), body: map[string]bool{"admin": admin}}, &updated); err != nil {
		return err
	}
	if bool(updated.Admin) != admin {
		return &adapter.Error{Kind: adapter.Upstream, Op: op, Resource: mxid, Status: http.StatusOK, Err: errors.New("admin bit unchanged; the server has no admin room to join")}
	}
	return nil
}

// ListSessions is undeclared: Synapse has no device listing across accounts.
func (a *Adapter) ListSessions(context.Context, adapter.ListQuery) (adapter.Page[adapter.Session], error) {
	return adapter.Page[adapter.Session]{}, adapter.NotSupportedError("sessions.list_all")
}

func (a *Adapter) ListAccountSessions(ctx context.Context, accountID string) ([]adapter.Session, error) {
	const op = "sessions.list_by_account"
	mxid, err := a.mxid(op, accountID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Devices []device `json:"devices"`
	}
	if _, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodGet, path: userPath(mxid) + "/devices"}, &out); err != nil {
		return nil, err
	}
	sessions := make([]adapter.Session, len(out.Devices))
	for i, d := range out.Devices {
		sessions[i] = d.session(mxid)
	}
	return sessions, nil
}

func (a *Adapter) TerminateSession(ctx context.Context, accountID, sessionID string) error {
	const op = "sessions.terminate"
	if accountID == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: sessionID, Err: errors.New("account is required to delete a device")}
	}
	mxid, err := a.mxid(op, accountID)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: mxid, Err: errors.New("device id is required")}
	}
	_, err = a.call(ctx, request{
		op: op, resource: mxid + "/" + sessionID, method: http.MethodDelete,
		path: userPath(mxid) + "/devices/" + url.PathEscape(sessionID),
	}, nil)
	return err
}

func (a *Adapter) TerminateAccountSessions(ctx context.Context, accountID string) error {
	const op = "sessions.terminate_all"
	sessions, err := a.ListAccountSessions(ctx, accountID)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return nil
	}
	mxid := sessions[0].AccountID
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	_, err = a.call(ctx, request{
		op: op, resource: mxid, method: http.MethodPost,
		path: userPath(mxid) + "/delete_devices",
		body: map[string][]string{"devices": ids},
	}, nil)
	return err
}

func (a *Adapter) ListRooms(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Room], error) {
	page, err := a.listRooms(ctx, "rooms.list", q)
	if err != nil {
		return adapter.Page[adapter.Room]{}, err
	}
	rooms := make([]adapter.Room, len(page.Rooms))
	for i, r := range page.Rooms {
		rooms[i] = r.room()
	}
	total := page.TotalRooms
	return adapter.Page[adapter.Room]{Items: rooms, Next: string(page.NextBatch), Total: &total}, nil
}

func (a *Adapter) listRooms(ctx context.Context, op string, q adapter.ListQuery) (roomPage, error) {
	from, err := offset(op, q.Cursor)
	if err != nil {
		return roomPage{}, err
	}
	query := url.Values{"from": {strconv.Itoa(from)}, "limit": {strconv.Itoa(limitOf(q))}}
	// An empty search_term is rejected upstream, so it is only sent when set.
	if q.Search != "" {
		query.Set("search_term", q.Search)
	}
	var page roomPage
	_, err = a.call(ctx, request{op: op, method: http.MethodGet, path: "/_synapse/admin/v1/rooms", query: query}, &page)
	return page, err
}

func (a *Adapter) GetRoom(ctx context.Context, id string) (*adapter.Room, error) {
	return a.roomDetails(ctx, "rooms.get", id)
}

func (a *Adapter) roomDetails(ctx context.Context, op, id string) (*adapter.Room, error) {
	if !validRoomID(id) {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: id, Err: errors.New("room id must be !opaque or !opaque:server")}
	}
	var r room
	if _, err := a.call(ctx, request{op: op, resource: id, method: http.MethodGet, path: "/_synapse/admin/v1/rooms/" + url.PathEscape(id)}, &r); err != nil {
		return nil, err
	}
	out := r.room()
	return &out, nil
}

// CreateRoom is undeclared: the admin API has no room creation, and creating
// one through the client API would act as the admin user.
func (a *Adapter) CreateRoom(context.Context, adapter.CreateRoom) (*adapter.Room, error) {
	return nil, adapter.NotSupportedError("rooms.create")
}

// DeleteRoom schedules the v2 purge, which runs in the background; the room
// leaves listings when that task completes. The v2 endpoint accepts any
// well-formed id, so existence is checked first to answer NotFound.
func (a *Adapter) DeleteRoom(ctx context.Context, id string) error {
	const op = "rooms.delete"
	if _, err := a.roomDetails(ctx, op, id); err != nil {
		return err
	}
	var out struct {
		DeleteID string `json:"delete_id"`
	}
	_, err := a.call(ctx, request{
		op: op, resource: id, method: http.MethodDelete,
		path: "/_synapse/admin/v2/rooms/" + url.PathEscape(id),
		body: map[string]bool{"purge": true, "block": false},
	}, &out)
	return err
}

// lifecycle says where an account lifecycle operation goes: through MAS when
// authentication is delegated and MAS credentials exist, nowhere when it is
// delegated without them (the capability is undeclared), else to Synapse.
func (a *Adapter) lifecycle(op string) (viaMAS bool, err error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.authMode != AuthModeMAS {
		return false, nil
	}
	if a.mas == nil {
		return false, adapter.NotSupportedError(op)
	}
	if !a.passwords && (op == "accounts.create" || op == "accounts.set_password") {
		return false, adapter.NotSupportedError(op)
	}
	return true, nil
}

func (a *Adapter) lifecycleTarget(op, id string) (viaMAS bool, mxid string, err error) {
	if viaMAS, err = a.lifecycle(op); err != nil {
		return false, "", err
	}
	mxid, err = a.mxid(op, id)
	return viaMAS, mxid, err
}

func (a *Adapter) delegated() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.authMode == AuthModeMAS && a.mas != nil
}

// masUser resolves an account in MAS by localpart; a deactivated one counts
// as missing, as on the Synapse side.
func (a *Adapter) masUser(ctx context.Context, op, mxid string) (*masUser, error) {
	localpart, _ := adapter.SplitMXID(mxid)
	var out struct {
		Data masUser `json:"data"`
	}
	if _, err := a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodGet, path: "/api/admin/v1/users/by-username/" + url.PathEscape(localpart)}, &out); err != nil {
		return nil, err
	}
	if out.Data.Attributes.DeactivatedAt != nil {
		return nil, &adapter.Error{Kind: adapter.NotFound, Op: op, Resource: mxid, Status: http.StatusOK, Err: errors.New("account is deactivated")}
	}
	return &out.Data, nil
}

// masAdmins lists the localparts MAS lets request admin, following the
// admin API's pagination links.
func (a *Adapter) masAdmins(ctx context.Context, op string) (map[string]bool, error) {
	admins := map[string]bool{}
	path := "/api/admin/v1/users?filter[admin]=true&page[first]=100"
	for path != "" {
		var page struct {
			Data  []masUser `json:"data"`
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
		}
		if _, err := a.mas.call(ctx, request{op: op, method: http.MethodGet, path: path}, &page); err != nil {
			return nil, err
		}
		for _, u := range page.Data {
			admins[u.Attributes.Username] = true
		}
		path = page.Links.Next
	}
	return admins, nil
}

// masAction resolves the account in MAS and posts one of its user actions.
func (a *Adapter) masAction(ctx context.Context, op, mxid, action string, body any) error {
	record, err := a.masUser(ctx, op, mxid)
	if err != nil {
		return err
	}
	_, err = a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodPost, path: masUserPath(record.ID) + action, body: body}, nil)
	return err
}

// masCreateAccount registers through MAS, which provisions the account into
// Synapse in the background, so the result is built from MAS's record. The
// password check is skipped: a rejection would leave a passwordless account.
func (a *Adapter) masCreateAccount(ctx context.Context, op, mxid string, req adapter.CreateAccount) (*adapter.Account, error) {
	localpart, _ := adapter.SplitMXID(mxid)
	body := map[string]any{"username": localpart}
	if req.DisplayName != "" {
		body["displayname"] = req.DisplayName
	}
	var created struct {
		Data masUser `json:"data"`
	}
	if _, err := a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodPost, path: "/api/admin/v1/users", body: body}, &created); err != nil {
		return nil, err
	}
	// From here on a failure must not leave a live account without the
	// password or admin bit that was asked for; the id stays taken either way.
	password := map[string]any{"password": req.Password, "skip_password_check": true}
	if _, err := a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodPost, path: masUserPath(created.Data.ID) + "/set-password", body: password}, nil); err != nil {
		return nil, a.masAbandon(ctx, op, mxid, created.Data.ID, err)
	}
	if req.Admin {
		if _, err := a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodPost, path: masUserPath(created.Data.ID) + "/set-admin", body: map[string]bool{"admin": true}}, nil); err != nil {
			return nil, a.masAbandon(ctx, op, mxid, created.Data.ID, err)
		}
	}
	return &adapter.Account{
		ID: mxid, Localpart: localpart, Domain: a.cfg.Domain, DisplayName: req.DisplayName,
		Enabled: true, Admin: req.Admin, Matrix: &adapter.MatrixAccountFacts{},
	}, nil
}

// mxid accepts a full MXID or a bare localpart on the configured server_name.
func (a *Adapter) mxid(op, id string) (string, error) {
	if strings.HasPrefix(id, "@") {
		if _, domain := adapter.SplitMXID(id); domain == "" {
			return "", &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: id, Err: errors.New("account id must be @localpart:server")}
		}
		return id, nil
	}
	if id == "" || strings.ContainsAny(id, "@:") {
		return "", &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: id, Err: errors.New("account id must be @localpart:server")}
	}
	return "@" + id + ":" + a.cfg.Domain, nil
}

// validRoomID accepts !opaque:server and, since room version 12, !opaque
// with no server part.
func validRoomID(id string) bool {
	return len(id) > 1 && strings.HasPrefix(id, "!") && !strings.ContainsAny(id, "/ ")
}

func userPath(mxid string) string {
	return "/_synapse/admin/v2/users/" + url.PathEscape(mxid)
}

func conflict(op, mxid string) error {
	return &adapter.Error{Kind: adapter.Conflict, Op: op, Resource: mxid, Status: http.StatusOK, Err: errors.New("account already exists")}
}

func offset(op, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(cursor)
	if err != nil || n < 0 {
		return 0, &adapter.Error{Kind: adapter.Invalid, Op: op, Err: errors.New("invalid cursor")}
	}
	return n, nil
}

func limitOf(q adapter.ListQuery) int {
	if q.Limit <= 0 {
		return adapter.DefaultLimit
	}
	return q.Limit
}

type userPage struct {
	Users     []user    `json:"users"`
	NextToken pageToken `json:"next_token"`
	Total     int       `json:"total"`
}

type user struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayname"`
	Admin        flag   `json:"admin"`
	Deactivated  flag   `json:"deactivated"`
	Locked       flag   `json:"locked"`
	Suspended    *flag  `json:"suspended"` // only the single-account query carries it
	ShadowBanned flag   `json:"shadow_banned"`
	Erased       flag   `json:"erased"`
	UserType     string `json:"user_type"`
	CreationTS   int64  `json:"creation_ts"`
	LastSeenTS   *int64 `json:"last_seen_ts"`
}

func (u user) account() adapter.Account {
	localpart, domain := adapter.SplitMXID(u.Name)
	acc := adapter.Account{
		ID:          u.Name,
		Localpart:   localpart,
		Domain:      domain,
		DisplayName: u.DisplayName,
		Enabled:     !bool(u.Deactivated) && !bool(u.Locked),
		Admin:       bool(u.Admin),
		Matrix: &adapter.MatrixAccountFacts{
			Deactivated:  bool(u.Deactivated),
			Locked:       bool(u.Locked),
			ShadowBanned: bool(u.ShadowBanned),
			Erased:       bool(u.Erased),
			UserType:     u.UserType,
		},
	}
	if u.Suspended != nil {
		suspended := bool(*u.Suspended)
		acc.Matrix.Suspended = &suspended
	}
	if u.CreationTS > 0 {
		t := timestamp(u.CreationTS)
		acc.CreatedAt = &t
	}
	if u.LastSeenTS != nil && *u.LastSeenTS > 0 {
		t := timestamp(*u.LastSeenTS)
		acc.LastSeen = &t
	}
	return acc
}

type device struct {
	DeviceID    string `json:"device_id"`
	DisplayName string `json:"display_name"`
	LastSeenIP  string `json:"last_seen_ip"`
	LastSeenUA  string `json:"last_seen_user_agent"`
	LastSeenTS  *int64 `json:"last_seen_ts"`
}

// session presents a device as a session that is never live: a device is a
// persistent login, not a connection.
func (d device) session(mxid string) adapter.Session {
	s := adapter.Session{ID: d.DeviceID, AccountID: mxid, Name: d.DisplayName, IP: d.LastSeenIP, UserAgent: d.LastSeenUA}
	if d.LastSeenTS != nil && *d.LastSeenTS > 0 {
		t := timestamp(*d.LastSeenTS)
		s.LastSeen = &t
	}
	return s
}

type roomPage struct {
	Rooms      []room    `json:"rooms"`
	NextBatch  pageToken `json:"next_batch"`
	TotalRooms int       `json:"total_rooms"`
}

type room struct {
	RoomID             string `json:"room_id"`
	Name               string `json:"name"`
	CanonicalAlias     string `json:"canonical_alias"`
	JoinedMembers      int    `json:"joined_members"`
	JoinedLocalMembers int    `json:"joined_local_members"`
	Version            string `json:"version"`
	Creator            string `json:"creator"`
	Encryption         string `json:"encryption"`
	Federatable        flag   `json:"federatable"`
	Public             flag   `json:"public"`
	Topic              string `json:"topic"`
}

func (r room) room() adapter.Room {
	return adapter.Room{
		ID:      r.RoomID,
		Name:    r.Name,
		Alias:   r.CanonicalAlias,
		Members: r.JoinedMembers,
		Public:  bool(r.Public),
		Matrix: &adapter.MatrixRoomFacts{
			Version:            r.Version,
			Creator:            r.Creator,
			Encryption:         r.Encryption,
			Federatable:        bool(r.Federatable),
			JoinedLocalMembers: r.JoinedLocalMembers,
			Topic:              r.Topic,
		},
	}
}

// masAbandon deactivates a half-created account and returns the cause; a
// failed deactivation is reported alongside it rather than hidden.
func (a *Adapter) masAbandon(ctx context.Context, op, mxid, ulid string, cause error) error {
	_, err := a.mas.call(ctx, request{op: op, resource: mxid, method: http.MethodPost, path: masUserPath(ulid) + "/deactivate", body: map[string]bool{"skip_erase": true}}, nil)
	if err != nil {
		return fmt.Errorf("%w (and deactivating the half-created account failed: %v)", cause, err)
	}
	return cause
}

// timestamp reads a Synapse timestamp: listings report milliseconds while
// the single-user query reports creation_ts in seconds, and 1e11 separates
// the two ranges for any date between 1973 and 5138.
func timestamp(v int64) time.Time {
	if v < 1e11 {
		return time.Unix(v, 0).UTC()
	}
	return time.UnixMilli(v).UTC()
}

// pageToken reads the next-page offset, which Synapse writes as a string for
// users and as a number for rooms.
type pageToken string

func (t *pageToken) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = pageToken(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*t = pageToken(n.String())
	return nil
}

// flag reads a boolean that older releases still encode as 0 or 1.
type flag bool

func (f *flag) UnmarshalJSON(data []byte) error {
	switch string(bytes.TrimSpace(data)) {
	case "true", "1":
		*f = true
	case "false", "0", "null":
		*f = false
	default:
		return fmt.Errorf("not a boolean: %s", data)
	}
	return nil
}

// A request is anonymous when the endpoint needs no token; under MAS Synapse
// validates any token presented, so an unauthenticated probe step must not
// carry one or a bad token fails at the wrong step.
type request struct {
	op, resource, method, path string
	query                      url.Values
	body                       any
	anonymous                  bool
	label                      string // shown instead of path in error text when path carries a secret
}

func (r request) shown() string {
	if r.label != "" {
		return r.label
	}
	return r.path
}

// call performs one request and maps a failure to *adapter.Error. The Matrix
// errcode decides the kind where the status alone is ambiguous; a 404 that
// carries no errcode did not come from Synapse and is an upstream error.
func (a *Adapter) call(ctx context.Context, req request, out any) (status int, err error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: req.op, Resource: req.resource}
	defer func() {
		if err != nil {
			failure.Err = err
			err = failure
		}
	}()

	target := a.baseURL + req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	var payload []byte
	if req.body != nil {
		if payload, err = json.Marshal(req.body); err != nil {
			return 0, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}
	header := http.Header{"Accept": {"application/json"}}
	if !req.anonymous {
		header.Set("Authorization", "Bearer "+a.cfg.Creds.Token)
	}
	if req.body != nil {
		header.Set("Content-Type", "application/json")
	}
	status, respBody, err := transport(ctx, a.httpClient, req.method, target, header, payload)
	if err != nil {
		if status == 0 {
			failure.Kind = adapter.Unreachable
			return 0, fmt.Errorf("failed to connect to server: %w", err)
		}
		failure.Status = status
		return status, fmt.Errorf("failed to read response: %w", err)
	}
	failure.Status = status
	if status >= 200 && status < 300 {
		if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return status, fmt.Errorf("unexpected response body: %w", err)
			}
		}
		return status, nil
	}

	var detail struct {
		Errcode      string `json:"errcode"`
		Error        string `json:"error"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	}
	_ = json.Unmarshal(respBody, &detail)
	failure.Code = detail.Errcode
	failure.Kind = classify(status, detail.Errcode)
	// Two 400s carry a meaning the errcode does not: a missing server_notices
	// block, and a registration token that already exists (M_INVALID_PARAM;
	// Tuwunel words it differently and prefixes the errcode).
	if status == http.StatusBadRequest {
		switch {
		case strings.Contains(detail.Error, "Server notices are not enabled"):
			failure.Kind = adapter.NotSupported
		case strings.Contains(strings.ToLower(detail.Error), "token already exists"):
			failure.Kind = adapter.Conflict
		}
	}
	if failure.Kind == adapter.RateLimited && detail.RetryAfterMS > 0 {
		failure.RetryAfter = time.Duration(detail.RetryAfterMS) * time.Millisecond
	}
	message := detail.Error
	if message == "" {
		message = snippet(string(respBody))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	return status, fmt.Errorf("%s %s: %s", req.method, req.shown(), message)
}

func classify(status int, errcode string) adapter.Kind {
	switch status {
	case http.StatusUnauthorized:
		return adapter.Unauthorized
	case http.StatusForbidden:
		return adapter.Forbidden
	case http.StatusNotFound:
		switch errcode {
		case "M_NOT_FOUND":
			return adapter.NotFound
		case "M_UNRECOGNIZED":
			return adapter.NotSupported
		}
		return adapter.Upstream
	case http.StatusConflict:
		return adapter.Conflict
	case http.StatusBadRequest:
		if errcode == "M_USER_IN_USE" {
			return adapter.Conflict
		}
		return adapter.Invalid
	case http.StatusTooManyRequests:
		return adapter.RateLimited
	}
	return adapter.Upstream
}

// snippet keeps the start of a non-JSON body, such as a proxy's HTML page,
// short enough for a log line.
func snippet(body string) string {
	body = strings.TrimSpace(body)
	const limit = 200
	if utf8.RuneCountInString(body) <= limit {
		return body
	}
	return string([]rune(body)[:limit]) + "..."
}
