package prosody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
)

// Adapter drives Prosody 13 through two HTTP surfaces on the same VirtualHost:
// upstream mod_http_admin_api under /admin_api for info and stats, and this
// repository's mod_admin_panel under /admin_panel for accounts and sessions.
type Adapter struct {
	cfg        adapter.ServerConfig
	httpClient *http.Client
	baseURL    string

	mu         sync.Mutex
	adminPanel bool // mod_admin_panel answered on the last Probe
}

var baseCapabilities = adapter.NewCapabilitySet(
	adapter.CapAccountsList, adapter.CapAccountsCreate, adapter.CapAccountsDelete,
	adapter.CapAccountsSetPassword, adapter.CapAccountsSetEnabled,
	adapter.CapSessionsListAll, adapter.CapSessionsListByAcct, adapter.CapSessionsTerminate,
)

func New(cfg adapter.ServerConfig) *Adapter {
	return &Adapter{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: http.DefaultTransport.(*http.Transport).Clone(),
		},
		baseURL: strings.TrimRight(cfg.Endpoint, "/"),
	}
}

// Probe reads /admin_api/server/info, which is the only routed endpoint that
// also validates the bearer token, then checks whether mod_admin_panel is
// mounted so the account and session capabilities are only declared when
// their endpoints exist.
func (a *Adapter) Probe(ctx context.Context) (*adapter.ServerInfo, error) {
	resp, err := a.doRequest(ctx, "server.probe", http.MethodGet, "/admin_api/server/info", nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		SiteName string `json:"site_name"`
		Version  string `json:"version"`
	}
	if err := json.Unmarshal(resp, &info); err != nil {
		return nil, a.parseError("server.probe", err)
	}

	mounted := true
	if _, err := a.doRequest(ctx, "server.probe", http.MethodGet, "/admin_panel/sessions", nil); err != nil {
		failure, _ := adapter.AsError(err)
		if failure == nil || failure.Status != http.StatusNotFound {
			return nil, err
		}
		mounted = false
	}
	a.mu.Lock()
	a.adminPanel = mounted
	a.mu.Unlock()

	domains := []string{a.cfg.Domain}
	if info.SiteName != "" && info.SiteName != a.cfg.Domain {
		domains = append(domains, info.SiteName)
	}
	return &adapter.ServerInfo{
		Protocol: adapter.ProtocolXMPP,
		Impl:     adapter.ImplProsody,
		Version:  info.Version,
		Domains:  domains,
	}, nil
}

func (a *Adapter) Capabilities() adapter.CapabilitySet {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.adminPanel {
		return adapter.NewCapabilitySet()
	}
	return baseCapabilities
}

func (a *Adapter) Close() error {
	a.httpClient.CloseIdleConnections()
	return nil
}

// Stats avoids /admin_api/server/metrics, which answers 500 on a default
// 13.0.5 install; counts come from the user and session listings instead.
func (a *Adapter) Stats(ctx context.Context) (*adapter.Stats, error) {
	resp, err := a.doRequest(ctx, "server.stats", http.MethodGet, "/admin_api/server/info", nil)
	if err != nil {
		return nil, err
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(resp, &info); err != nil {
		return nil, a.parseError("server.stats", err)
	}
	stats := &adapter.Stats{Version: info.Version}

	if usersResp, err := a.doRequest(ctx, "server.stats", http.MethodGet, "/admin_api/users", nil); err == nil {
		var users []json.RawMessage
		if json.Unmarshal(usersResp, &users) == nil {
			registered := len(users)
			stats.RegisteredUsers = &registered
		}
	}

	if a.Capabilities().Has(adapter.CapSessionsListAll) {
		if sessions, err := a.fetchSessions(ctx, "server.stats"); err == nil {
			online := map[string]struct{}{}
			for _, s := range sessions {
				online[s.AccountID] = struct{}{}
			}
			onlineUsers, active := len(online), len(sessions)
			stats.OnlineUsers = &onlineUsers
			stats.ActiveSessions = &active
		}
	}
	return stats, nil
}

// ListAccounts ignores q.Domain: mod_admin_panel is mounted per VirtualHost,
// so the listing is always the configured domain's.
func (a *Adapter) ListAccounts(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Account], error) {
	accounts, err := a.fetchAccounts(ctx, "accounts.list")
	if err != nil {
		return adapter.Page[adapter.Account]{}, err
	}
	return adapter.Paginate(accounts, q, func(acc adapter.Account) string { return acc.ID })
}

func (a *Adapter) GetAccount(ctx context.Context, id string) (*adapter.Account, error) {
	accounts, err := a.fetchAccounts(ctx, "accounts.get")
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i], nil
		}
	}
	return nil, &adapter.Error{Kind: adapter.NotFound, Op: "accounts.get", Resource: id, Err: errors.New("account not found")}
}

func (a *Adapter) CreateAccount(ctx context.Context, req adapter.CreateAccount) (*adapter.Account, error) {
	if req.Domain != "" && req.Domain != a.cfg.Domain {
		return nil, a.wrongDomain("accounts.create", req.Domain)
	}
	if req.Admin {
		return nil, adapter.NotSupportedError("accounts.set_admin")
	}
	body := map[string]string{"password": req.Password}
	resp, err := a.doRequest(ctx, "accounts.create", http.MethodPut, "/admin_panel/users/"+url.PathEscape(req.Localpart), body)
	if err != nil {
		return nil, err
	}
	var created struct {
		Username string `json:"username"`
		JID      string `json:"jid"`
	}
	if err := json.Unmarshal(resp, &created); err != nil || created.JID == "" {
		created.Username = req.Localpart
		created.JID = req.Localpart + "@" + a.cfg.Domain
	}
	return &adapter.Account{ID: created.JID, Localpart: created.Username, Domain: a.cfg.Domain, Enabled: true}, nil
}

func (a *Adapter) DeleteAccount(ctx context.Context, id string) error {
	localpart, err := a.localpart("accounts.delete", id)
	if err != nil {
		return err
	}
	_, err = a.doRequest(ctx, "accounts.delete", http.MethodDelete, "/admin_panel/users/"+url.PathEscape(localpart), nil)
	return err
}

func (a *Adapter) SetPassword(ctx context.Context, id, password string) error {
	localpart, err := a.localpart("accounts.set_password", id)
	if err != nil {
		return err
	}
	_, err = a.doRequest(ctx, "accounts.set_password", http.MethodPatch, "/admin_panel/users/"+url.PathEscape(localpart),
		map[string]string{"password": password})
	return err
}

func (a *Adapter) SetEnabled(ctx context.Context, id string, enabled bool) error {
	localpart, err := a.localpart("accounts.set_enabled", id)
	if err != nil {
		return err
	}
	_, err = a.doRequest(ctx, "accounts.set_enabled", http.MethodPatch, "/admin_panel/users/"+url.PathEscape(localpart),
		map[string]bool{"enabled": enabled})
	return err
}

func (a *Adapter) SetAdmin(context.Context, string, bool) error {
	return adapter.NotSupportedError("accounts.set_admin")
}

func (a *Adapter) ListSessions(ctx context.Context, q adapter.ListQuery) (adapter.Page[adapter.Session], error) {
	sessions, err := a.fetchSessions(ctx, "sessions.list")
	if err != nil {
		return adapter.Page[adapter.Session]{}, err
	}
	return adapter.Paginate(sessions, q, func(s adapter.Session) string { return s.ID })
}

func (a *Adapter) ListAccountSessions(ctx context.Context, accountID string) ([]adapter.Session, error) {
	sessions, err := a.fetchSessions(ctx, "sessions.list_by_account")
	if err != nil {
		return nil, err
	}
	out := make([]adapter.Session, 0)
	for _, s := range sessions {
		if s.AccountID == accountID {
			out = append(out, s)
		}
	}
	return out, nil
}

// TerminateSession closes one c2s session by full JID; the resource may hold
// slashes, so the path segment is escaped and Prosody decodes it again.
func (a *Adapter) TerminateSession(ctx context.Context, _, sessionID string) error {
	_, err := a.doRequest(ctx, "sessions.terminate", http.MethodDelete, "/admin_panel/sessions/"+url.PathEscape(sessionID), nil)
	return err
}

func (a *Adapter) TerminateAccountSessions(ctx context.Context, accountID string) error {
	localpart, err := a.localpart("sessions.terminate_all", accountID)
	if err != nil {
		return err
	}
	_, err = a.doRequest(ctx, "sessions.terminate_all", http.MethodPost, "/admin_panel/sessions/disconnect/"+url.PathEscape(localpart), nil)
	return err
}

// Prosody 13's admin API has no MUC endpoints, so rooms stay undeclared.

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

func (a *Adapter) fetchAccounts(ctx context.Context, op string) ([]adapter.Account, error) {
	resp, err := a.doRequest(ctx, op, http.MethodGet, "/admin_panel/users", nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Username string `json:"username"`
		JID      string `json:"jid"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, a.parseError(op, err)
	}
	accounts := make([]adapter.Account, len(raw))
	for i, u := range raw {
		jid := u.JID
		if jid == "" {
			jid = u.Username + "@" + a.cfg.Domain
		}
		accounts[i] = adapter.Account{
			ID:        jid,
			Localpart: u.Username,
			Domain:    a.cfg.Domain,
			Enabled:   u.Enabled == nil || *u.Enabled,
		}
	}
	return accounts, nil
}

func (a *Adapter) fetchSessions(ctx context.Context, op string) ([]adapter.Session, error) {
	resp, err := a.doRequest(ctx, op, http.MethodGet, "/admin_panel/sessions", nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		JID         string `json:"jid"`
		BareJID     string `json:"bare_jid"`
		Resource    string `json:"resource"`
		IPAddress   string `json:"ip_address"`
		Priority    int    `json:"priority"`
		Status      string `json:"status"`
		ConnectedAt string `json:"connected_at"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, a.parseError(op, err)
	}
	sessions := make([]adapter.Session, len(raw))
	for i, r := range raw {
		bare := r.BareJID
		if bare == "" {
			bare, _, _ = strings.Cut(r.JID, "/")
		}
		sessions[i] = adapter.Session{
			ID:        r.JID,
			AccountID: bare,
			Name:      r.Resource,
			IP:        r.IPAddress,
			Live:      true,
			XMPP:      &adapter.XMPPSessionFacts{Priority: r.Priority, Status: r.Status},
		}
		if t, err := time.Parse(time.RFC3339, r.ConnectedAt); err == nil {
			sessions[i].StartedAt = &t
		}
	}
	return sessions, nil
}

// localpart accepts a bare JID on this VirtualHost, or a bare localpart.
func (a *Adapter) localpart(op, id string) (string, error) {
	localpart, domain, _ := adapter.SplitJID(id)
	if localpart == "" {
		return domain, nil
	}
	if domain != a.cfg.Domain {
		return "", a.wrongDomain(op, domain)
	}
	return localpart, nil
}

func (a *Adapter) wrongDomain(op, domain string) error {
	return &adapter.Error{Kind: adapter.Invalid, Op: op, Resource: domain,
		Err: fmt.Errorf("this server only manages %s", a.cfg.Domain)}
}

func (a *Adapter) parseError(op string, err error) error {
	return &adapter.Error{Kind: adapter.Upstream, Op: op, Status: http.StatusOK, Err: fmt.Errorf("unexpected response body: %w", err)}
}

// doRequest sends the Host header as the configured domain, because
// mod_http_admin_api routes to a VirtualHost by Host and answers 404 for an
// address; the TCP connection itself goes to Endpoint.
func (a *Adapter) doRequest(ctx context.Context, op, method, path string, body interface{}) (_ []byte, err error) {
	failure := &adapter.Error{Kind: adapter.Upstream, Op: op, Resource: path}
	defer func() {
		if err != nil {
			failure.Err = err
			err = failure
		}
	}()

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Host = a.cfg.Domain
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

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return respBody, nil
	case http.StatusUnauthorized:
		failure.Kind = adapter.Unauthorized
		return nil, errors.New("authentication failed")
	case http.StatusForbidden:
		failure.Kind = adapter.Forbidden
		return nil, errors.New("token lacks admin permission")
	case http.StatusNotFound:
		// A leaf under a mounted collection is a missing resource; a missing
		// collection means the module is not installed, which stays Upstream
		// so the install gap is visible instead of reading as an empty server.
		if strings.HasPrefix(path, "/admin_panel/users/") || strings.HasPrefix(path, "/admin_panel/sessions/") {
			failure.Kind = adapter.NotFound
			return nil, errors.New("not found")
		}
		return nil, fmt.Errorf("endpoint not found at %s", path)
	case http.StatusConflict:
		failure.Kind = adapter.Conflict
		return nil, errors.New("already exists")
	case http.StatusBadRequest:
		failure.Kind = adapter.Invalid
		return nil, fmt.Errorf("rejected request: %s", strings.TrimSpace(string(respBody)))
	case http.StatusTooManyRequests:
		failure.Kind = adapter.RateLimited
		return nil, errors.New("rate limited")
	case http.StatusNotImplemented:
		failure.Kind = adapter.NotSupported
		return nil, errors.New("not supported by this Prosody version")
	default:
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
}
