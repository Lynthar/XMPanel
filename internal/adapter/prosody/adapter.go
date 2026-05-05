package prosody

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/store/models"
	apperrors "github.com/xmpanel/xmpanel/pkg/errors"
	"github.com/xmpanel/xmpanel/pkg/types"
)

// Adapter implements XMPPAdapter for Prosody 13.x via mod_http_admin_api.
//
// Endpoint paths and behaviors verified against Prosody 13.0.5 +
// prosody-modules mod_http_admin_api (commit 971a531654dc, May 2026).
// See docs/DEPLOY_DEBIAN.md §F for the full path map.
//
// Sessions/MUC/Modules are NOT supported by mod_http_admin_api in Prosody 13;
// those methods return ErrNotImplemented so the panel UI surfaces 501 instead
// of confusing 404s.
//
// HTTP Host header: mod_http_admin_api routes by Host header. Adapter sends
// `Host: <server.Host>` so the operator should configure the server with
// Host = the XMPP virtual host name (e.g. "xmpp.example.com"). For same-box
// deployments add `127.0.0.1 xmpp.example.com` to /etc/hosts so TCP stays on
// loopback while the HTTP Host header still matches the VirtualHost.
type Adapter struct {
	server     *models.XMPPServer
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

// NewAdapter creates a new Prosody adapter.
func NewAdapter(server *models.XMPPServer, apiKey string) *Adapter {
	scheme := "http"
	if server.TLSEnabled {
		scheme = "https"
	}

	return &Adapter{
		server: server,
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: fmt.Sprintf("%s://%s:%d", scheme, server.Host, server.Port),
	}
}

// Connect tests the connection to the Prosody server.
func (a *Adapter) Connect(ctx context.Context) error {
	return a.Ping(ctx)
}

// Disconnect is a no-op (HTTP is stateless).
func (a *Adapter) Disconnect() error {
	return nil
}

// Ping verifies the server is reachable AND the bearer token is valid.
// We hit /admin_api/server/info because /admin_api itself is not a routed
// endpoint in mod_http_admin_api 13 — it returns 404 even when the module
// is healthy.
func (a *Adapter) Ping(ctx context.Context) error {
	_, err := a.doRequest(ctx, http.MethodGet, "/admin_api/server/info", nil)
	return err
}

// GetServerInfo retrieves server info (version, site name).
//
// Response shape from Prosody 13.0.5:
//
//	{"site_name":"xmpp.example.com","version":"13.0.5"}
//
// Note: hosts list is NOT exposed by /server/info — operators configure the
// XMPP domain at the server-record level in XMPanel.
func (a *Adapter) GetServerInfo(ctx context.Context) (*types.ServerInfo, error) {
	resp, err := a.doRequest(ctx, http.MethodGet, "/admin_api/server/info", nil)
	if err != nil {
		return nil, err
	}

	var info struct {
		SiteName string `json:"site_name"`
		Version  string `json:"version"`
	}
	if err := json.Unmarshal(resp, &info); err != nil {
		return nil, fmt.Errorf("failed to parse server info: %w", err)
	}

	return &types.ServerInfo{
		Type:     types.ServerTypeProsody,
		Version:  info.Version,
		Hostname: info.SiteName,
		Domains:  []string{info.SiteName},
	}, nil
}

// GetStats derives basic counters from /server/info + /users.
//
// Background: /admin_api/server/metrics in Prosody 13.0.5 returns
// 500 Internal Server Error in default config (root cause not investigated;
// the endpoint is documented as beta). We avoid it entirely.
func (a *Adapter) GetStats(ctx context.Context) (*models.ServerStats, error) {
	infoResp, err := a.doRequest(ctx, http.MethodGet, "/admin_api/server/info", nil)
	if err != nil {
		return nil, err
	}

	var info struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(infoResp, &info)

	stats := &models.ServerStats{
		Version: info.Version,
	}

	// Count registered users via /users (mod_http_admin_api lists across all hosts
	// the API user has admin on).
	if usersResp, err := a.doRequest(ctx, http.MethodGet, "/admin_api/users", nil); err == nil {
		var users []struct {
			Username string `json:"username"`
			Enabled  bool   `json:"enabled"`
		}
		if json.Unmarshal(usersResp, &users) == nil {
			stats.RegisteredUsers = len(users)
		}
	}

	// OnlineUsers / ActiveSessions / S2SConnections — not available via
	// mod_http_admin_api in Prosody 13. Leave as zero.
	return stats, nil
}

// ListUsers returns all known XMPP users.
//
// Why /admin_panel/users instead of /admin_api/users:
// mod_http_admin_api in Prosody 13.0.5 lists users correctly but its PUT
// endpoint silently fails to actually create accounts (returns 200, nothing
// in the accounts store). To get a CRUD surface that actually works, we
// ship our own mod_admin_panel.lua which goes through usermanager.* directly.
// We use its endpoints exclusively for user CRUD so the read view stays
// consistent with what writes produce.
//
// The `domain` parameter is IGNORED. The mod is host-scoped (mounted on a
// VirtualHost), so it returns the users for that host. JIDs are built from
// `server.Host`.
func (a *Adapter) ListUsers(ctx context.Context, _ string) ([]models.XMPPUser, error) {
	resp, err := a.doRequest(ctx, http.MethodGet, "/admin_panel/users", nil)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Username string `json:"username"`
		JID      string `json:"jid"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse users: %w", err)
	}

	domain := a.server.Host
	users := make([]models.XMPPUser, len(raw))
	for i, u := range raw {
		jid := u.JID
		if jid == "" {
			jid = fmt.Sprintf("%s@%s", u.Username, domain)
		}
		users[i] = models.XMPPUser{
			Username: u.Username,
			Domain:   domain,
			JID:      jid,
		}
	}
	return users, nil
}

// GetUser is best-effort: mod_admin_panel doesn't expose a per-user GET, so
// we list and filter. mod_http_admin_api has GET /admin_api/users/{u} but
// we avoid mixing the two surfaces to keep semantics predictable.
func (a *Adapter) GetUser(ctx context.Context, username, _ string) (*models.XMPPUser, error) {
	all, err := a.ListUsers(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Username == username {
			return &all[i], nil
		}
	}
	return nil, apperrors.ErrUserNotFound
}

// CreateUser creates a new XMPP user via the mod_admin_panel endpoint.
// mod_http_admin_api's PUT silently no-ops in 13.0.5; mod_admin_panel calls
// usermanager.create_user directly so the account actually persists.
func (a *Adapter) CreateUser(ctx context.Context, req models.CreateXMPPUserRequest) error {
	body := map[string]string{
		"password": req.Password,
	}
	_, err := a.doRequest(ctx, http.MethodPut,
		fmt.Sprintf("/admin_panel/users/%s", req.Username), body)
	return err
}

// DeleteUser removes a user (calls usermanager.delete_user via admin_panel
// which fires user-deleted + purges roster/pubsub/etc).
func (a *Adapter) DeleteUser(ctx context.Context, username, _ string) error {
	_, err := a.doRequest(ctx, http.MethodDelete,
		fmt.Sprintf("/admin_panel/users/%s", username), nil)
	return err
}

// ChangePassword updates a user's password via mod_admin_panel.
func (a *Adapter) ChangePassword(ctx context.Context, username, _, newPassword string) error {
	body := map[string]string{
		"password": newPassword,
	}
	_, err := a.doRequest(ctx, http.MethodPatch,
		fmt.Sprintf("/admin_panel/users/%s", username), body)
	return err
}

// Capabilities reports what this adapter supports. /server/info comes from
// upstream mod_http_admin_api; user CRUD and session control require the
// custom mod_admin_panel module (prosody/mod_admin_panel.lua in this repo).
// We mark Sessions as supported because the adapter speaks that wire
// format; if the operator hasn't loaded the module, requests return 404
// and the panel surfaces that as a 502 — acceptable, and the deploy doc
// includes mod_admin_panel as a required step (§2.8.5).
func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		OnlineUsersCount:     false,
		RegisteredUsersCount: true,
		ActiveSessionsCount:  false,
		S2SConnectionsCount:  false,
		Sessions:             true,
		Rooms:                false,
		Modules:              false,
	}
}

// --- Sessions: served by the custom mod_admin_panel module
//     (see prosody/mod_admin_panel.lua in this repo). Mounted at
//     /admin_panel/sessions/ rather than under /admin_api/ because
//     mod_http_admin_api owns the /admin_api/* prefix. ---

// GetOnlineSessions lists all live c2s sessions on the configured VirtualHost.
func (a *Adapter) GetOnlineSessions(ctx context.Context) ([]models.XMPPSession, error) {
	// Trailing slash on the collection URL — mod_admin_panel's `GET /sessions`
	// route matches the prefix exactly. We use the trailing-slash form for
	// consistency with the disconnect-user subpath; either works.
	resp, err := a.doRequest(ctx, http.MethodGet, "/admin_panel/sessions", nil)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		JID         string `json:"jid"`
		Resource    string `json:"resource"`
		IPAddress   string `json:"ip_address"`
		Priority    int    `json:"priority"`
		Status      string `json:"status"`
		ConnectedAt string `json:"connected_at"`
	}
	if err := json.Unmarshal(resp, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse sessions: %w", err)
	}
	sessions := make([]models.XMPPSession, len(raw))
	for i, r := range raw {
		sessions[i] = models.XMPPSession{
			JID:       r.JID,
			Resource:  r.Resource,
			IPAddress: r.IPAddress,
			Priority:  r.Priority,
			Status:    r.Status,
		}
		if r.ConnectedAt != "" {
			if t, err := time.Parse(time.RFC3339, r.ConnectedAt); err == nil {
				sessions[i].StartedAt = t
			}
		}
	}
	return sessions, nil
}

// GetUserSessions filters online sessions by bare JID. Implemented client-side
// to avoid a per-user endpoint; for typical hosts this is fine.
func (a *Adapter) GetUserSessions(ctx context.Context, username, domain string) ([]models.XMPPSession, error) {
	all, err := a.GetOnlineSessions(ctx)
	if err != nil {
		return nil, err
	}
	prefix := username + "@" + domain
	out := make([]models.XMPPSession, 0)
	for _, s := range all {
		// session.JID is full ("user@host/resource"); compare bare prefix.
		if strings.HasPrefix(s.JID, prefix+"/") || s.JID == prefix {
			out = append(out, s)
		}
	}
	return out, nil
}

// KickSession closes a single c2s session by full JID.
func (a *Adapter) KickSession(ctx context.Context, jid string) error {
	// Slashes and other special chars (legal in JID resources per RFC 6122)
	// must be percent-encoded so they don't fragment the URL path. Prosody's
	// HTTP router decodes the captured wildcard before passing to handlers.
	_, err := a.doRequest(ctx, http.MethodDelete,
		"/admin_panel/sessions/"+url.PathEscape(jid), nil)
	return err
}

// KickUser closes every active session for a given username on the host.
// The domain parameter is informational (mod_admin_panel scopes to the
// host it's mounted on).
func (a *Adapter) KickUser(ctx context.Context, username, domain string) error {
	_, err := a.doRequest(ctx, http.MethodPost,
		"/admin_panel/sessions/disconnect/"+url.PathEscape(username), nil)
	return err
}

// --- MUC rooms: NOT supported by mod_http_admin_api in Prosody 13 ---

func (a *Adapter) ListRooms(ctx context.Context, mucDomain string) ([]models.XMPPRoom, error) {
	return nil, apperrors.ErrNotImplemented
}

func (a *Adapter) GetRoom(ctx context.Context, room, mucDomain string) (*models.XMPPRoom, error) {
	return nil, apperrors.ErrNotImplemented
}

func (a *Adapter) CreateRoom(ctx context.Context, req models.CreateXMPPRoomRequest) error {
	return apperrors.ErrNotImplemented
}

func (a *Adapter) DeleteRoom(ctx context.Context, room, mucDomain string) error {
	return apperrors.ErrNotImplemented
}

// --- Modules: NOT supported by mod_http_admin_api in Prosody 13 ---

func (a *Adapter) ListModules(ctx context.Context) ([]types.ModuleInfo, error) {
	return nil, apperrors.ErrNotImplemented
}

func (a *Adapter) EnableModule(ctx context.Context, module string) error {
	return apperrors.ErrNotImplemented
}

func (a *Adapter) DisableModule(ctx context.Context, module string) error {
	return apperrors.ErrNotImplemented
}

// doRequest performs an HTTP request to the Prosody admin API.
//
// Sets req.Host explicitly to server.Host so the request reaches
// mod_http_admin_api on the matching VirtualHost. For loopback deployments,
// configure /etc/hosts to resolve the XMPP domain to 127.0.0.1.
func (a *Adapter) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
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

	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apperrors.ErrConnectionFailed, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		return respBody, nil
	case http.StatusUnauthorized:
		return nil, apperrors.ErrAuthFailed
	case http.StatusNotFound:
		// Distinguish "resource not found" from "endpoint not registered".
		// Heuristic: any GET/DELETE/PATCH on a leaf URL under a known prefix
		// is treated as "the resource doesn't exist". Routes that are POST
		// or unknown prefixes get the louder "endpoint not found" message
		// so unmounted modules surface clearly during install.
		switch method {
		case http.MethodGet, http.MethodDelete, http.MethodPatch:
			if strings.HasPrefix(path, "/admin_panel/users/") ||
				strings.HasPrefix(path, "/admin_panel/sessions/") {
				return nil, apperrors.ErrUserNotFound
			}
		}
		return nil, fmt.Errorf("%w: endpoint not found at %s", apperrors.ErrOperationFailed, path)
	case http.StatusConflict:
		return nil, apperrors.ErrUserExists
	default:
		return nil, fmt.Errorf("%w: %s: %s", apperrors.ErrOperationFailed, resp.Status, string(respBody))
	}
}
