package ejabberd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Adapter drives ejabberd through mod_http_api: every command is a POST of a
// JSON argument object to /api/<command>, and results follow the API v1+
// encoding where a named scalar arrives bare.
type Adapter struct {
	cfg        adapter.ServerConfig
	httpClient *http.Client
	baseURL    string
}

var capabilities = adapter.NewCapabilitySet(
	adapter.CapAccountsList, adapter.CapAccountsGet, adapter.CapAccountsCreate, adapter.CapAccountsDelete, adapter.CapAccountsSetPassword,
	adapter.CapSessionsListAll, adapter.CapSessionsListByAcct, adapter.CapSessionsTerminate,
	adapter.CapRoomsList, adapter.CapRoomsGet, adapter.CapRoomsCreate, adapter.CapRoomsDelete,
)

var versionPattern = regexp.MustCompile(`ejabberd\s+([0-9][^\s]*)`)

func New(cfg adapter.ServerConfig) *Adapter {
	return &Adapter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		baseURL: strings.TrimRight(cfg.Endpoint, "/") + "/api",
	}
}

func (a *Adapter) Probe(ctx context.Context) (*adapter.ServerInfo, error) {
	statusResp, err := a.doRequest(ctx, "server.probe", "status", nil)
	if err != nil {
		return nil, err
	}
	var status string
	if err := json.Unmarshal(statusResp, &status); err != nil {
		return nil, a.parseError("server.probe", err)
	}
	version := status
	if m := versionPattern.FindStringSubmatch(status); m != nil {
		version = m[1]
	}

	hostsResp, err := a.doRequest(ctx, "server.probe", "registered_vhosts", nil)
	if err != nil {
		return nil, err
	}
	var hosts []string
	if err := json.Unmarshal(hostsResp, &hosts); err != nil {
		return nil, a.parseError("server.probe", err)
	}
	return &adapter.ServerInfo{
		Protocol: adapter.ProtocolXMPP,
		Impl:     adapter.ImplEjabberd,
		Version:  version,
		Domains:  hosts,
	}, nil
}

func (a *Adapter) Capabilities() adapter.CapabilitySet {
	return capabilities
}

func (a *Adapter) Close() error {
	a.httpClient.CloseIdleConnections()
	return nil
}

// Stats reads one counter per command; a counter whose command fails is left
// nil rather than reported as zero.
func (a *Adapter) Stats(ctx context.Context) (*adapter.Stats, error) {
	stats := &adapter.Stats{}
	if v, err := a.intCommand(ctx, "server.stats", "connected_users_number", nil); err == nil {
		stats.OnlineUsers = &v
	}
	if v, err := a.intCommand(ctx, "server.stats", "stats", map[string]string{"name": "registeredusers"}); err == nil {
		stats.RegisteredUsers = &v
	}
	if v, err := a.intCommand(ctx, "server.stats", "stats", map[string]string{"name": "uptimeseconds"}); err == nil {
		uptime := int64(v)
		stats.UptimeSeconds = &uptime
	}
	if v, err := a.intCommand(ctx, "server.stats", "incoming_s2s_number", nil); err == nil {
		stats.S2SConnections = &v
	}
	if stats.OnlineUsers == nil && stats.RegisteredUsers == nil {
		if _, err := a.doRequest(ctx, "server.stats", "status", nil); err != nil {
			return nil, err
		}
	}
	return stats, nil
}

func (a *Adapter) ListAccounts(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Account], error) {
	domain := q.Domain
	if domain == "" {
		domain = a.cfg.Domain
	}
	resp, err := a.doRequest(ctx, "accounts.list", "registered_users", map[string]string{"host": domain})
	if err != nil {
		return adapter.Page[adapter.Account]{}, err
	}
	var usernames []string
	if err := json.Unmarshal(resp, &usernames); err != nil {
		return adapter.Page[adapter.Account]{}, a.parseError("accounts.list", err)
	}
	accounts := make([]adapter.Account, len(usernames))
	for i, username := range usernames {
		accounts[i] = adapter.Account{ID: username + "@" + domain, Localpart: username, Domain: domain, Enabled: true}
	}
	return adapter.Paginate(accounts, q, func(acc adapter.Account) string { return acc.ID })
}

func (a *Adapter) GetAccount(ctx context.Context, id string) (*adapter.Account, error) {
	localpart, domain := a.splitAccount(id)
	exists, err := a.intCommand(ctx, "accounts.get", "check_account", map[string]string{"user": localpart, "host": domain})
	if err != nil {
		return nil, err
	}
	if exists != 0 {
		return nil, &adapter.Error{Kind: adapter.NotFound, Op: "accounts.get", Resource: id, Err: errors.New("account not found")}
	}
	return &adapter.Account{ID: localpart + "@" + domain, Localpart: localpart, Domain: domain, Enabled: true}, nil
}

func (a *Adapter) CreateAccount(ctx context.Context, req adapter.CreateAccount) (*adapter.Account, error) {
	if req.Admin {
		return nil, adapter.NotSupportedError("accounts.set_admin")
	}
	domain := req.Domain
	if domain == "" {
		domain = a.cfg.Domain
	}
	_, err := a.doRequest(ctx, "accounts.create", "register", map[string]string{
		"user": req.Localpart, "host": domain, "password": req.Password,
	})
	if err != nil {
		return nil, err
	}
	return &adapter.Account{ID: req.Localpart + "@" + domain, Localpart: req.Localpart, Domain: domain, Enabled: true}, nil
}

func (a *Adapter) DeleteAccount(ctx context.Context, id string) error {
	localpart, domain := a.splitAccount(id)
	_, err := a.doRequest(ctx, "accounts.delete", "unregister", map[string]string{"user": localpart, "host": domain})
	return err
}

func (a *Adapter) SetPassword(ctx context.Context, id, password string) error {
	localpart, domain := a.splitAccount(id)
	_, err := a.doRequest(ctx, "accounts.set_password", "change_password", map[string]string{
		"user": localpart, "host": domain, "newpass": password,
	})
	return err
}

// SetEnabled and SetAdmin stay undeclared: ejabberd's ban_account rewrites
// the password and admin rights live in the ACL config, neither of which is
// a reversible per-account switch.

func (a *Adapter) SetEnabled(context.Context, string, bool) error {
	return adapter.NotSupportedError("accounts.set_enabled")
}

func (a *Adapter) SetAdmin(context.Context, string, bool) error {
	return adapter.NotSupportedError("accounts.set_admin")
}

func (a *Adapter) ListSessions(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Session], error) {
	resp, err := a.doRequest(ctx, "sessions.list", "connected_users_info", nil)
	if err != nil {
		return adapter.Page[adapter.Session]{}, err
	}
	var raw []sessionInfo
	if err := json.Unmarshal(resp, &raw); err != nil {
		return adapter.Page[adapter.Session]{}, a.parseError("sessions.list", err)
	}
	// connected_users_info carries the full JID in one "jid" field and has
	// no "user" or "server" keys.
	sessions := make([]adapter.Session, len(raw))
	for i, s := range raw {
		sessions[i] = s.session(s.JID)
	}
	if q.Domain != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if _, domain, _ := adapter.SplitJID(s.AccountID); domain == q.Domain {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}
	return adapter.Paginate(sessions, q, func(s adapter.Session) string { return s.ID })
}

func (a *Adapter) ListAccountSessions(ctx context.Context, accountID string) ([]adapter.Session, error) {
	localpart, domain := a.splitAccount(accountID)
	resp, err := a.doRequest(ctx, "sessions.list_by_account", "user_sessions_info", map[string]string{"user": localpart, "host": domain})
	if err != nil {
		return nil, err
	}
	var raw []sessionInfo
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, a.parseError("sessions.list_by_account", err)
	}
	sessions := make([]adapter.Session, len(raw))
	for i, s := range raw {
		sessions[i] = s.session(localpart + "@" + domain + "/" + s.Resource)
	}
	return sessions, nil
}

func (a *Adapter) TerminateSession(ctx context.Context, _, sessionID string) error {
	localpart, domain, resource := adapter.SplitJID(sessionID)
	if localpart == "" || resource == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: "sessions.terminate", Resource: sessionID, Err: errors.New("session id must be a full JID")}
	}
	_, err := a.doRequest(ctx, "sessions.terminate", "kick_session", map[string]string{
		"user": localpart, "host": domain, "resource": resource, "reason": "Kicked by administrator",
	})
	return err
}

func (a *Adapter) TerminateAccountSessions(ctx context.Context, accountID string) error {
	localpart, domain := a.splitAccount(accountID)
	_, err := a.doRequest(ctx, "sessions.terminate_all", "kick_user", map[string]string{"user": localpart, "host": domain})
	return err
}

// ListRooms asks every MUC service unless q.Domain names one. muc_online_rooms
// returns full room JIDs; options and occupants come from GetRoom per room,
// and one unreadable room degrades to its name rather than failing the list.
func (a *Adapter) ListRooms(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Room], error) {
	service := q.Domain
	if service == "" {
		service = "global"
	}
	resp, err := a.doRequest(ctx, "rooms.list", "muc_online_rooms", map[string]string{"service": service})
	if err != nil {
		return adapter.Page[adapter.Room]{}, err
	}
	var roomJIDs []string
	if err := json.Unmarshal(resp, &roomJIDs); err != nil {
		return adapter.Page[adapter.Room]{}, a.parseError("rooms.list", err)
	}
	page, err := adapter.Paginate(roomJIDs, q, func(jid string) string { return jid })
	if err != nil {
		return adapter.Page[adapter.Room]{}, err
	}
	rooms := make([]adapter.Room, len(page.Items))
	for i, jid := range page.Items {
		room, err := a.GetRoom(ctx, jid)
		if err != nil {
			name, _, _ := strings.Cut(jid, "@")
			rooms[i] = adapter.Room{ID: jid, Name: name}
			continue
		}
		rooms[i] = *room
	}
	return adapter.Page[adapter.Room]{Items: rooms, Next: page.Next, Total: page.Total}, nil
}

func (a *Adapter) GetRoom(ctx context.Context, id string) (*adapter.Room, error) {
	name, service, hasService := strings.Cut(id, "@")
	if !hasService || name == "" {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: "rooms.get", Resource: id, Err: errors.New("room id must be room@service")}
	}
	// The argument stays "name": upstream renames it for newer releases and
	// older ones only accept the original.
	resp, err := a.doRequest(ctx, "rooms.get", "get_room_options", map[string]string{"name": name, "service": service})
	if err != nil {
		return nil, err
	}
	options, err := roomOptions(resp)
	if err != nil {
		return nil, a.parseError("rooms.get", err)
	}
	// A missing room answers 200 with no options at all rather than an error.
	if len(options) == 0 {
		return nil, &adapter.Error{Kind: adapter.NotFound, Op: "rooms.get", Resource: id, Err: errors.New("room not found")}
	}
	room := &adapter.Room{ID: id, Name: name, Public: options["public"] == "true", XMPP: &adapter.XMPPRoomFacts{
		Description: options["description"],
		Persistent:  options["persistent"] == "true",
		MembersOnly: options["members_only"] == "true",
		Moderated:   options["moderated"] == "true",
	}}
	if title := options["title"]; title != "" {
		room.Name = title
	}
	if count, err := a.intCommand(ctx, "rooms.get", "get_room_occupants_number", map[string]string{"name": name, "service": service}); err == nil {
		room.Members = count
	}
	return room, nil
}

// roomOptions accepts both encodings of get_room_options: a list of
// {name, value} pairs (API v1+) or one object keyed by option name (v0).
func roomOptions(body []byte) (map[string]string, error) {
	var pairs []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if json.Unmarshal(body, &pairs) == nil {
		options := make(map[string]string, len(pairs))
		for _, p := range pairs {
			options[p.Name] = p.Value
		}
		return options, nil
	}
	var options map[string]string
	if err := json.Unmarshal(body, &options); err != nil {
		return nil, fmt.Errorf("unexpected get_room_options shape: %w", err)
	}
	return options, nil
}

func (a *Adapter) CreateRoom(ctx context.Context, req adapter.CreateRoom) (*adapter.Room, error) {
	if req.Domain == "" {
		return nil, &adapter.Error{Kind: adapter.Invalid, Op: "rooms.create", Err: errors.New("MUC service domain is required")}
	}
	_, err := a.doRequest(ctx, "rooms.create", "create_room", map[string]string{
		"name": req.Name, "service": req.Domain, "host": a.cfg.Domain,
	})
	if err != nil {
		return nil, err
	}
	options := [][2]string{
		{"title", req.Name},
		{"description", req.Description},
		{"public", fmt.Sprintf("%t", req.Public)},
		{"persistent", fmt.Sprintf("%t", req.Persistent)},
		{"members_only", fmt.Sprintf("%t", req.MembersOnly)},
	}
	for _, opt := range options {
		if _, err := a.doRequest(ctx, "rooms.create", "change_room_option", map[string]string{
			"name": req.Name, "service": req.Domain, "option": opt[0], "value": opt[1],
		}); err != nil {
			return nil, err
		}
	}
	return a.GetRoom(ctx, req.Name+"@"+req.Domain)
}

func (a *Adapter) DeleteRoom(ctx context.Context, id string) error {
	name, service, hasService := strings.Cut(id, "@")
	if !hasService || name == "" {
		return &adapter.Error{Kind: adapter.Invalid, Op: "rooms.delete", Resource: id, Err: errors.New("room id must be room@service")}
	}
	_, err := a.doRequest(ctx, "rooms.delete", "destroy_room", map[string]string{"name": name, "service": service})
	return err
}

type sessionInfo struct {
	JID      string `json:"jid"`
	Resource string `json:"resource"`
	IP       string `json:"ip"`
	Priority int    `json:"priority"`
	Status   string `json:"status"`
	Uptime   int64  `json:"uptime"`
}

func (s sessionInfo) session(fullJID string) adapter.Session {
	bare, _, _ := strings.Cut(fullJID, "/")
	out := adapter.Session{
		ID:        fullJID,
		AccountID: bare,
		Name:      s.Resource,
		IP:        s.IP,
		Live:      true,
		XMPP:      &adapter.XMPPSessionFacts{Priority: s.Priority, Status: s.Status},
	}
	if s.Uptime > 0 {
		started := time.Now().Add(-time.Duration(s.Uptime) * time.Second)
		out.StartedAt = &started
	}
	return out
}

// splitAccount accepts a bare JID or a bare localpart on the configured domain.
func (a *Adapter) splitAccount(id string) (localpart, domain string) {
	localpart, domain, _ = adapter.SplitJID(id)
	if localpart == "" {
		return domain, a.cfg.Domain
	}
	return localpart, domain
}

// intCommand reads a single-integer result. Depending on the API version the
// unversioned /api URL selects, mod_http_api sends it bare (v1+) or wrapped
// in a one-key object such as {"stat": 3} (v0, the default on 23.x), so both
// shapes are accepted.
func (a *Adapter) intCommand(ctx context.Context, op, command string, args map[string]string) (int, error) {
	resp, err := a.doRequest(ctx, op, command, args)
	if err != nil {
		return 0, err
	}
	var value int
	if json.Unmarshal(resp, &value) == nil {
		return value, nil
	}
	var wrapped map[string]int
	if err := json.Unmarshal(resp, &wrapped); err != nil || len(wrapped) != 1 {
		return 0, a.parseError(op, fmt.Errorf("not an integer result: %s", strings.TrimSpace(string(resp))))
	}
	for _, v := range wrapped {
		value = v
	}
	return value, nil
}

func (a *Adapter) parseError(op string, err error) error {
	return &adapter.Error{Kind: adapter.Upstream, Op: op, Status: http.StatusOK, Err: fmt.Errorf("unexpected response body: %w", err)}
}

// doRequest maps mod_http_api's status codes: 409 and 404 come from
// command-level conflict and not_found errors, 400 from argument checks,
// 500 from every other command failure. The JSON body's message is kept.
func (a *Adapter) doRequest(ctx context.Context, op, command string, args map[string]string) (_ []byte, err error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: op, Resource: command}
	defer func() {
		if err != nil {
			failure.Err = err
			err = failure
		}
	}()

	// mod_http_api answers 400 to a POST without a JSON body even for
	// commands that take no arguments, so an empty object is always sent.
	if args == nil {
		args = map[string]string{}
	}
	jsonBody, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/"+command, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Creds.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		failure.Kind = adapter.Unreachable
		return nil, fmt.Errorf("failed to connect to server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	failure.Status = resp.StatusCode

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return respBody, nil
	}

	message := strings.TrimSpace(string(respBody))
	var detail struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(respBody, &detail) == nil && detail.Message != "" {
		message = detail.Message
		failure.Code = fmt.Sprint(detail.Code)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		failure.Kind = adapter.Unauthorized
	case http.StatusForbidden:
		failure.Kind = adapter.Forbidden
	case http.StatusNotFound:
		failure.Kind = adapter.NotFound
	case http.StatusConflict:
		failure.Kind = adapter.Conflict
	case http.StatusBadRequest:
		failure.Kind = adapter.Invalid
	case http.StatusTooManyRequests:
		failure.Kind = adapter.RateLimited
	case http.StatusInternalServerError:
		// mod_muc_admin raises plain {error, Text} for missing or duplicate
		// rooms, which mod_http_api answers as 500; the text is the only clue.
		lower := strings.ToLower(message)
		switch {
		case strings.Contains(lower, "not found") || strings.Contains(lower, "does not exist") || strings.Contains(lower, "unknown_user"):
			failure.Kind = adapter.NotFound
		case strings.Contains(lower, "already exist") || strings.Contains(lower, "already registered"):
			failure.Kind = adapter.Conflict
		}
	}
	return nil, fmt.Errorf("%s: %s", command, message)
}
