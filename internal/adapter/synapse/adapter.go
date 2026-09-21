package synapse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// Under MAS delegation the account lifecycle belongs to MAS, so Probe narrows
// the capability set instead of writing where MAS would not see it.
type Adapter struct {
	cfg        adapter.ServerConfig
	httpClient *http.Client
	baseURL    string
	mu         sync.RWMutex
	authMode   string
	caps       adapter.CapabilitySet
}

const (
	AuthModeLegacy = "legacy"
	AuthModeMAS    = "mas"
)

var legacyCapabilities = adapter.NewCapabilitySet(
	adapter.CapAccountsList, adapter.CapAccountsSearch, adapter.CapAccountsCreate, adapter.CapAccountsDelete,
	adapter.CapAccountsSetPassword, adapter.CapAccountsSetEnabled, adapter.CapAccountsSetAdmin,
	adapter.CapSessionsListByAcct, adapter.CapSessionsTerminate,
	adapter.CapRoomsList, adapter.CapRoomsGet, adapter.CapRoomsDelete,
)

// lifecycle names the operations MAS owns once authentication is delegated.
var lifecycle = []adapter.Capability{
	adapter.CapAccountsCreate, adapter.CapAccountsDelete, adapter.CapAccountsSetPassword,
	adapter.CapAccountsSetEnabled, adapter.CapAccountsSetAdmin,
}

func New(cfg adapter.ServerConfig) *Adapter {
	return &Adapter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		baseURL:  strings.TrimRight(cfg.Endpoint, "/"),
		authMode: AuthModeLegacy,
		caps:     legacyCapabilities,
	}
}

// Probe checks reachability, the token, the delegation mode and admin rights
// in that order, so the first failure names the layer that is wrong.
func (a *Adapter) Probe(ctx context.Context) (*adapter.ServerInfo, error) {
	const op = "server.probe"
	var versions struct {
		Versions []string `json:"versions"`
	}
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_matrix/client/versions"}, &versions); err != nil {
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

	authMode := AuthModeLegacy
	if _, err := a.call(ctx, request{op: op, method: http.MethodGet, path: "/_matrix/client/v1/auth_metadata"}, nil); err != nil {
		if failure, ok := adapter.AsError(err); !ok || failure.Status != http.StatusNotFound {
			return nil, err
		}
	} else {
		authMode = AuthModeMAS
	}

	version, err := a.serverVersion(ctx, op)
	if err != nil {
		return nil, err
	}
	caps := legacyCapabilities
	if authMode == AuthModeMAS {
		caps = legacyCapabilities.Without(lifecycle...)
	}
	a.mu.Lock()
	a.authMode, a.caps = authMode, caps
	a.mu.Unlock()
	return &adapter.ServerInfo{
		Protocol: adapter.ProtocolMatrix,
		Impl:     adapter.ImplSynapse,
		Version:  version,
		Domains:  []string{serverName},
		AuthMode: authMode,
	}, nil
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
	if err := a.lifecycleAllowed(op); err != nil {
		return nil, err
	}
	if req.Domain != "" && req.Domain != a.cfg.Domain {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: req.Domain, Err: fmt.Errorf("server only serves %s", a.cfg.Domain)}
	}
	if req.Localpart == "" || strings.ContainsAny(req.Localpart, "@:") {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: req.Localpart, Err: errors.New("localpart is empty or contains @ or a colon")}
	}
	mxid := "@" + req.Localpart + ":" + a.cfg.Domain
	// PUT creates or modifies, so a taken id must be detected first: a
	// deactivated account keeps its id forever and must not be revived here.
	var existing user
	_, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodGet, path: userPath(mxid)}, &existing)
	if err == nil {
		return nil, conflict(op, mxid)
	}
	if failure, ok := adapter.AsError(err); !ok || failure.Kind != adapter.NotFound {
		return nil, err
	}
	body := map[string]any{"password": req.Password, "admin": req.Admin}
	if req.DisplayName != "" {
		body["displayname"] = req.DisplayName
	}
	var created user
	status, err := a.call(ctx, request{op: op, resource: mxid, method: http.MethodPut, path: userPath(mxid), body: body}, &created)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, conflict(op, mxid)
	}
	acc := created.account()
	return &acc, nil
}

// DeleteAccount deactivates without erasing; Matrix has no deletion, and the
// erasing form is a MatrixAdmin operation behind its own permission.
func (a *Adapter) DeleteAccount(ctx context.Context, id string) error {
	const op = "accounts.delete"
	if err := a.lifecycleAllowed(op); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
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

func (a *Adapter) SetPassword(ctx context.Context, id, password string) error {
	return a.modify(ctx, "accounts.set_password", id, map[string]any{"password": password, "logout_devices": true})
}

// SetEnabled maps to Synapse's lock: a locked account cannot log in and its
// clients are soft-logged-out, and unlocking restores it unchanged.
func (a *Adapter) SetEnabled(ctx context.Context, id string, enabled bool) error {
	return a.modify(ctx, "accounts.set_enabled", id, map[string]any{"locked": !enabled})
}

// modify applies fields through the create-or-modify endpoint after checking
// the account exists, because the same PUT would otherwise create it.
func (a *Adapter) modify(ctx context.Context, op, id string, fields map[string]any) error {
	if err := a.lifecycleAllowed(op); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
	}
	if _, err := a.user(ctx, op, mxid); err != nil {
		return err
	}
	_, err = a.call(ctx, request{op: op, resource: mxid, method: http.MethodPut, path: userPath(mxid), body: fields}, nil)
	return err
}

func (a *Adapter) SetAdmin(ctx context.Context, id string, admin bool) error {
	const op = "accounts.set_admin"
	if err := a.lifecycleAllowed(op); err != nil {
		return err
	}
	mxid, err := a.mxid(op, id)
	if err != nil {
		return err
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
	if !strings.HasPrefix(id, "!") || !strings.Contains(id, ":") {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: id, Err: errors.New("room id must be !id:server")}
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

// lifecycleAllowed refuses the account operations MAS owns while the panel
// has no MAS client, so the declared capability set and behaviour agree.
func (a *Adapter) lifecycleAllowed(op string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.authMode == AuthModeMAS {
		return adapter.NotSupportedError(op)
	}
	return nil
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

type request struct {
	op, resource, method, path string
	query                      url.Values
	body                       any
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
	var payload io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal request body: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.method, target, payload)
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.Creds.Token)
	httpReq.Header.Set("Accept", "application/json")
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		failure.Kind = adapter.Unreachable
		return 0, fmt.Errorf("failed to connect to server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	failure.Status = resp.StatusCode

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return resp.StatusCode, fmt.Errorf("unexpected response body: %w", err)
			}
		}
		return resp.StatusCode, nil
	}

	var detail struct {
		Errcode      string `json:"errcode"`
		Error        string `json:"error"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	}
	_ = json.Unmarshal(respBody, &detail)
	failure.Code = detail.Errcode
	failure.Kind = classify(resp.StatusCode, detail.Errcode)
	if failure.Kind == adapter.RateLimited && detail.RetryAfterMS > 0 {
		failure.RetryAfter = time.Duration(detail.RetryAfterMS) * time.Millisecond
	}
	message := detail.Error
	if message == "" {
		message = snippet(string(respBody))
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return resp.StatusCode, fmt.Errorf("%s %s: %s", req.method, req.path, message)
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
