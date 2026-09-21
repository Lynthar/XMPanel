// Package matrixgeneric drives any Matrix homeserver through the client
// spec alone: one admin token, lock and suspend where m.account_moderation
// is advertised, whois for connections, and no listing of anything.
package matrixgeneric

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/matrixhttp"
)

// Adapter is what the panel can do on a server it knows nothing about: look
// one account up, lock or suspend it where the server allows, and read its
// connections. The probe decides which of those apply.
type Adapter struct {
	cfg        adapter.ServerConfig
	httpClient *http.Client
	client     *matrixhttp.Client
	mu         sync.RWMutex
	lock       bool
	suspend    bool
	whois      bool
	vendor     string // "continuwuity" or "tuwunel": the user-count endpoint is theirs
	caps       adapter.CapabilitySet
}

var _ adapter.MatrixAdmin = (*Adapter)(nil)

func New(cfg adapter.ServerConfig) *Adapter {
	a := &Adapter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		caps: adapter.NewCapabilitySet(adapter.CapAccountsGet),
	}
	a.client = &matrixhttp.Client{HTTP: a.httpClient, BaseURL: strings.TrimRight(cfg.Endpoint, "/"), Token: cfg.Creds.Token}
	return a
}

// Probe checks reachability, the token and the server name, then reads what
// the server advertises: m.account_moderation for lock and suspend, a
// version from federation or a vendor route, and whether whois is served.
func (a *Adapter) Probe(ctx context.Context) (*adapter.ServerInfo, error) {
	const op = "server.probe"
	if a.cfg.Creds.MAS != nil {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Err: errors.New("MAS credentials apply to Synapse only")}
	}
	var versions struct {
		Versions []string `json:"versions"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_matrix/client/versions", Anonymous: true}, &versions); err != nil {
		return nil, err
	}
	if len(versions.Versions) == 0 {
		return nil, &adapter.Error{Kind: adapter.Upstream, Op: op, Status: http.StatusOK, Err: errors.New("endpoint is not a Matrix homeserver")}
	}
	var who struct {
		UserID string `json:"user_id"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_matrix/client/v3/account/whoami"}, &who); err != nil {
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

	var caps struct {
		Capabilities struct {
			Moderation struct {
				Lock    bool `json:"lock"`
				Suspend bool `json:"suspend"`
			} `json:"m.account_moderation"`
		} `json:"capabilities"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_matrix/client/v3/capabilities"}, &caps); err != nil {
		return nil, err
	}
	name, version := a.version(ctx, op)
	whois, err := a.servesWhois(ctx, op, who.UserID)
	if err != nil {
		return nil, err
	}

	set := adapter.NewCapabilitySet(adapter.CapAccountsGet)
	moderation := caps.Capabilities.Moderation
	if moderation.Lock {
		set[adapter.CapAccountsSetEnabled] = struct{}{}
	}
	if moderation.Suspend {
		set[adapter.CapMatrixSuspend] = struct{}{}
	}
	if whois {
		set[adapter.CapSessionsListByAcct] = struct{}{}
	}
	a.mu.Lock()
	a.lock, a.suspend, a.whois, a.vendor, a.caps = moderation.Lock, moderation.Suspend, whois, vendorOf(name), set
	a.mu.Unlock()
	return &adapter.ServerInfo{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplMatrixGeneric,
		Version:  version,
		Domains:  []string{serverName},
	}, nil
}

// version asks the federation API, which is public but may not be routed to
// the client endpoint or may be disabled, then the vendor routes Continuwuity
// and Tuwunel serve unconditionally. No answer leaves the version empty.
func (a *Adapter) version(ctx context.Context, op string) (name, version string) {
	var federation struct {
		Server struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"server"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_matrix/federation/v1/version", Anonymous: true}, &federation); err == nil {
		return federation.Server.Name, federation.Server.Version
	}
	for _, path := range []string{"/_continuwuity/server_version", "/_tuwunel/server_version"} {
		var vendor struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: path, Anonymous: true}, &vendor); err == nil && vendor.Version != "" {
			return vendor.Name, vendor.Version
		}
	}
	return "", ""
}

// servesWhois tries the admin's own record: an unserved route or a token
// without admin rights leave the capability out, anything else is a failure.
func (a *Adapter) servesWhois(ctx context.Context, op, mxid string) (bool, error) {
	_, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: whoisPath(mxid)}, nil)
	if err == nil {
		return true, nil
	}
	if failure, ok := adapter.AsError(err); ok && (failure.Kind == adapter.NotSupported || failure.Kind == adapter.Forbidden || failure.Status == http.StatusNotFound) {
		return false, nil
	}
	return false, err
}

func vendorOf(name string) string {
	switch lower := strings.ToLower(name); {
	case strings.Contains(lower, "continuwuity"):
		return "continuwuity"
	case strings.Contains(lower, "tuwunel"):
		return "tuwunel"
	}
	return ""
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

// Stats carries the version and, on Continuwuity or Tuwunel with federation
// enabled, the local user count; every other figure is unknown.
func (a *Adapter) Stats(ctx context.Context) (*adapter.Stats, error) {
	const op = "server.stats"
	_, version := a.version(ctx, op)
	stats := &adapter.Stats{Version: version}
	a.mu.RLock()
	vendor := a.vendor
	a.mu.RUnlock()
	if vendor != "" {
		var count struct {
			Count int `json:"count"`
		}
		if _, err := a.call(ctx, matrixhttp.Request{Op: op, Method: http.MethodGet, Path: "/_" + vendor + "/local_user_count", Anonymous: true}, &count); err == nil {
			stats.RegisteredUsers = &count.Count
		}
	}
	return stats, nil
}

func (a *Adapter) ListAccounts(context.Context, adapter.ListQuery) (adapter.Page[adapter.Account], error) {
	return adapter.Page[adapter.Account]{}, adapter.NotSupportedError("accounts.list")
}

// GetAccount reads the moderation flags first, since the spec makes them
// answer 404 for a missing or deactivated account, then the profile for the
// display name. Without m.account_moderation only the profile is known.
func (a *Adapter) GetAccount(ctx context.Context, id string) (*adapter.Account, error) {
	const op = "accounts.get"
	mxid, err := a.mxid(op, id)
	if err != nil {
		return nil, err
	}
	localpart, _ := adapter.SplitMXID(mxid)
	acc := adapter.Account{ID: mxid, Localpart: localpart, Domain: a.cfg.Domain, Enabled: true, Matrix: &adapter.MatrixAccountFacts{}}
	a.mu.RLock()
	lock, suspend := a.lock, a.suspend
	a.mu.RUnlock()
	if lock {
		locked, err := a.flag(ctx, op, mxid, "lock", "locked")
		if err != nil {
			return nil, err
		}
		acc.Enabled, acc.Matrix.Locked = !locked, locked
	}
	if suspend {
		suspended, err := a.flag(ctx, op, mxid, "suspend", "suspended")
		if err != nil {
			return nil, err
		}
		acc.Matrix.Suspended = &suspended
	}
	var profile struct {
		DisplayName string `json:"displayname"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: profilePath(mxid)}, &profile); err != nil {
		return nil, err
	}
	acc.DisplayName = profile.DisplayName
	return &acc, nil
}

// flag reads one moderation switch: GET /_matrix/client/v1/admin/{action}/{id}
// answers {"<field>": bool}.
func (a *Adapter) flag(ctx context.Context, op, mxid, action, field string) (bool, error) {
	var out map[string]bool
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: moderationPath(action, mxid)}, &out); err != nil {
		return false, err
	}
	return out[field], nil
}

// moderate sets one switch through PUT with the same body shape.
func (a *Adapter) moderate(ctx context.Context, op, mxid, action, field string, value bool) error {
	_, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodPut, Path: moderationPath(action, mxid), Body: map[string]bool{field: value}}, nil)
	return err
}

func (a *Adapter) CreateAccount(context.Context, adapter.CreateAccount) (*adapter.Account, error) {
	return nil, adapter.NotSupportedError("accounts.create")
}

func (a *Adapter) DeleteAccount(context.Context, string) error {
	return adapter.NotSupportedError("accounts.delete")
}

func (a *Adapter) SetPassword(context.Context, string, string) error {
	return adapter.NotSupportedError("accounts.set_password")
}

// SetEnabled is the spec lock: a locked account cannot log in and its
// clients are told to log out softly; unlocking restores it unchanged.
func (a *Adapter) SetEnabled(ctx context.Context, id string, enabled bool) error {
	const op = "accounts.set_enabled"
	if err := a.declared(op, adapter.CapAccountsSetEnabled); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
	}
	return a.moderate(ctx, op, mxid, "lock", "locked", !enabled)
}

func (a *Adapter) SetAdmin(context.Context, string, bool) error {
	return adapter.NotSupportedError("accounts.set_admin")
}

func (a *Adapter) ListSessions(context.Context, adapter.ListQuery) (adapter.Page[adapter.Session], error) {
	return adapter.Page[adapter.Session]{}, adapter.NotSupportedError("sessions.list_all")
}

// ListAccountSessions flattens whois into one session per connection, keyed
// by device id where the server gives one (Synapse and Tuwunel do not). An
// empty answer is checked against the profile: whois is 200 for a typo too.
func (a *Adapter) ListAccountSessions(ctx context.Context, accountID string) ([]adapter.Session, error) {
	const op = "sessions.list_by_account"
	if err := a.declared(op, adapter.CapSessionsListByAcct); err != nil {
		return nil, err
	}
	mxid, err := a.mxid(op, accountID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Devices map[string]struct {
			Sessions []struct {
				Connections []struct {
					IP        string `json:"ip"`
					LastSeen  *int64 `json:"last_seen"`
					UserAgent string `json:"user_agent"`
				} `json:"connections"`
			} `json:"sessions"`
		} `json:"devices"`
	}
	if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: whoisPath(mxid)}, &out); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(out.Devices))
	for key := range out.Devices {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var sessions []adapter.Session
	for _, key := range keys {
		for _, s := range out.Devices[key].Sessions {
			for _, c := range s.Connections {
				id := fmt.Sprintf("connection-%d", len(sessions)+1)
				if key != "" {
					id = key + "/" + id
				}
				session := adapter.Session{ID: id, AccountID: mxid, Name: key, IP: c.IP, UserAgent: c.UserAgent}
				if c.LastSeen != nil && *c.LastSeen > 0 {
					t := time.UnixMilli(*c.LastSeen).UTC()
					session.LastSeen = &t
				}
				sessions = append(sessions, session)
			}
		}
	}
	if len(sessions) == 0 {
		if _, err := a.call(ctx, matrixhttp.Request{Op: op, Resource: mxid, Method: http.MethodGet, Path: profilePath(mxid)}, nil); err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

func (a *Adapter) TerminateSession(context.Context, string, string) error {
	return adapter.NotSupportedError("sessions.terminate")
}

func (a *Adapter) TerminateAccountSessions(context.Context, string) error {
	return adapter.NotSupportedError("sessions.terminate_all")
}

func (a *Adapter) ListRooms(context.Context, adapter.ListQuery) (adapter.Page[adapter.Room], error) {
	return adapter.Page[adapter.Room]{}, adapter.NotSupportedError("rooms.list")
}

func (a *Adapter) GetRoom(context.Context, string) (*adapter.Room, error) {
	return nil, adapter.NotSupportedError("rooms.get")
}

func (a *Adapter) CreateRoom(context.Context, adapter.CreateRoom) (*adapter.Room, error) {
	return nil, adapter.NotSupportedError("rooms.create")
}

func (a *Adapter) DeleteRoom(context.Context, string) error {
	return adapter.NotSupportedError("rooms.delete")
}

// declared answers NotSupported before any request for an operation the
// probe left out of the capability set.
func (a *Adapter) declared(op string, c adapter.Capability) error {
	if !a.Capabilities().Has(c) {
		return adapter.NotSupportedError(op)
	}
	return nil
}

func (a *Adapter) call(ctx context.Context, req matrixhttp.Request, out any) (int, error) {
	return a.client.Call(ctx, req, out)
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

func moderationPath(action, mxid string) string {
	return "/_matrix/client/v1/admin/" + action + "/" + url.PathEscape(mxid)
}

func whoisPath(mxid string) string {
	return "/_matrix/client/v3/admin/whois/" + url.PathEscape(mxid)
}

func profilePath(mxid string) string {
	return "/_matrix/client/v3/profile/" + url.PathEscape(mxid)
}
