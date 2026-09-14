package ejabberd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/store/models"
	apperrors "github.com/xmpanel/xmpanel/pkg/errors"
	"github.com/xmpanel/xmpanel/pkg/types"
)

// Adapter implements XMPPAdapter for ejabberd servers
// It uses the ejabberd REST API (mod_http_api)
type Adapter struct {
	server     *models.XMPPServer
	apiKey     string
	httpClient *http.Client
	baseURL    string
}

// NewAdapter creates a new ejabberd adapter
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
		baseURL: fmt.Sprintf("%s://%s:%d/api", scheme, server.Host, server.Port),
	}
}

// Connect tests the connection to the ejabberd server
func (a *Adapter) Connect(ctx context.Context) error {
	return a.Ping(ctx)
}

// Disconnect closes the connection (no-op for HTTP)
func (a *Adapter) Disconnect() error {
	return nil
}

// Ping checks if the server is reachable
func (a *Adapter) Ping(ctx context.Context) error {
	_, err := a.doRequest(ctx, "status", nil)
	return err
}

// GetServerInfo retrieves server information
func (a *Adapter) GetServerInfo(ctx context.Context) (*types.ServerInfo, error) {
	// Get version
	versionResp, err := a.doRequest(ctx, "status", nil)
	if err != nil {
		return nil, err
	}

	var version string
	if err := json.Unmarshal(versionResp, &version); err != nil {
		return nil, fmt.Errorf("failed to parse version: %w", err)
	}

	// Get hosts
	hostsResp, err := a.doRequest(ctx, "registered_vhosts", nil)
	if err != nil {
		return nil, err
	}

	var hosts []string
	if err := json.Unmarshal(hostsResp, &hosts); err != nil {
		return nil, fmt.Errorf("failed to parse hosts: %w", err)
	}

	return &types.ServerInfo{
		Type:    types.ServerTypeEjabberd,
		Version: version,
		Domains: hosts,
	}, nil
}

// GetStats retrieves server statistics
func (a *Adapter) GetStats(ctx context.Context) (*models.ServerStats, error) {
	stats := &models.ServerStats{
		Extra: make(map[string]interface{}),
	}

	// All three commands return one named integer, which mod_http_api sends as
	// a bare JSON number on API v1+ (what an unversioned /api URL selects);
	// parsing `stats` as {"stat": N} left registered users at 0 everywhere.
	if resp, err := a.doRequest(ctx, "connected_users_number", nil); err == nil {
		var count int
		if json.Unmarshal(resp, &count) == nil {
			stats.OnlineUsers = count
		}
	}

	if resp, err := a.doRequest(ctx, "stats", map[string]string{"name": "registeredusers"}); err == nil {
		var count int
		if json.Unmarshal(resp, &count) == nil {
			stats.RegisteredUsers = count
		}
	}

	if resp, err := a.doRequest(ctx, "incoming_s2s_number", nil); err == nil {
		var count int
		if json.Unmarshal(resp, &count) == nil {
			stats.S2SConnections = count
		}
	}

	return stats, nil
}

// ListUsers lists all users in a domain
func (a *Adapter) ListUsers(ctx context.Context, domain string) ([]models.XMPPUser, error) {
	resp, err := a.doRequest(ctx, "registered_users", map[string]string{"host": domain})
	if err != nil {
		return nil, err
	}

	var usernames []string
	if err := json.Unmarshal(resp, &usernames); err != nil {
		return nil, fmt.Errorf("failed to parse users: %w", err)
	}

	users := make([]models.XMPPUser, len(usernames))
	for i, username := range usernames {
		users[i] = models.XMPPUser{
			Username: username,
			Domain:   domain,
			JID:      fmt.Sprintf("%s@%s", username, domain),
		}
	}

	return users, nil
}

// GetUser retrieves information about a specific user
func (a *Adapter) GetUser(ctx context.Context, username, domain string) (*models.XMPPUser, error) {
	// Check if user exists
	resp, err := a.doRequest(ctx, "check_account", map[string]string{
		"user": username,
		"host": domain,
	})
	if err != nil {
		return nil, err
	}

	var exists int
	if err := json.Unmarshal(resp, &exists); err != nil {
		return nil, fmt.Errorf("failed to parse check_account result: %w", err)
	}
	if exists != 0 {
		return nil, apperrors.ErrUserNotFound
	}

	// Get user sessions
	sessionsResp, err := a.doRequest(ctx, "user_sessions_info", map[string]string{
		"user": username,
		"host": domain,
	})

	var sessions []map[string]interface{}
	var resources []string
	if err == nil && json.Unmarshal(sessionsResp, &sessions) == nil {
		for _, s := range sessions {
			if r, ok := s["resource"].(string); ok {
				resources = append(resources, r)
			}
		}
	}

	return &models.XMPPUser{
		Username:  username,
		Domain:    domain,
		JID:       fmt.Sprintf("%s@%s", username, domain),
		Online:    len(resources) > 0,
		Resources: resources,
	}, nil
}

// CreateUser creates a new user
func (a *Adapter) CreateUser(ctx context.Context, req models.CreateXMPPUserRequest) error {
	_, err := a.doRequest(ctx, "register", map[string]string{
		"user":     req.Username,
		"host":     req.Domain,
		"password": req.Password,
	})
	return err
}

// DeleteUser deletes a user
func (a *Adapter) DeleteUser(ctx context.Context, username, domain string) error {
	_, err := a.doRequest(ctx, "unregister", map[string]string{
		"user": username,
		"host": domain,
	})
	return err
}

// ChangePassword changes a user's password
func (a *Adapter) ChangePassword(ctx context.Context, username, domain, newPassword string) error {
	_, err := a.doRequest(ctx, "change_password", map[string]string{
		"user":    username,
		"host":    domain,
		"newpass": newPassword,
	})
	return err
}

// GetOnlineSessions retrieves all online sessions
func (a *Adapter) GetOnlineSessions(ctx context.Context) ([]models.XMPPSession, error) {
	resp, err := a.doRequest(ctx, "connected_users_info", nil)
	if err != nil {
		return nil, err
	}

	var rawSessions []map[string]interface{}
	if err := json.Unmarshal(resp, &rawSessions); err != nil {
		return nil, fmt.Errorf("failed to parse sessions: %w", err)
	}

	// connected_users_info carries the full JID in one "jid" field and has no
	// "user" or "server" key, so assembling the JID from those yielded
	// "@/<resource>" for every session.
	sessions := make([]models.XMPPSession, len(rawSessions))
	for i, s := range rawSessions {
		sessions[i] = models.XMPPSession{
			JID:       getString(s, "jid"),
			Resource:  getString(s, "resource"),
			IPAddress: getString(s, "ip"),
			Priority:  getInt(s, "priority"),
			Status:    getString(s, "status"),
		}
	}

	return sessions, nil
}

// GetUserSessions retrieves sessions for a specific user
func (a *Adapter) GetUserSessions(ctx context.Context, username, domain string) ([]models.XMPPSession, error) {
	resp, err := a.doRequest(ctx, "user_sessions_info", map[string]string{
		"user": username,
		"host": domain,
	})
	if err != nil {
		return nil, err
	}

	var rawSessions []map[string]interface{}
	if err := json.Unmarshal(resp, &rawSessions); err != nil {
		return nil, fmt.Errorf("failed to parse sessions: %w", err)
	}

	sessions := make([]models.XMPPSession, len(rawSessions))
	for i, s := range rawSessions {
		sessions[i] = models.XMPPSession{
			JID:       fmt.Sprintf("%s@%s/%s", username, domain, getString(s, "resource")),
			Resource:  getString(s, "resource"),
			IPAddress: getString(s, "ip"),
			Priority:  getInt(s, "priority"),
			Status:    getString(s, "status"),
		}
	}

	return sessions, nil
}

// KickSession disconnects a specific session
func (a *Adapter) KickSession(ctx context.Context, jid string) error {
	// Parse JID
	user, server, resource := parseJID(jid)
	_, err := a.doRequest(ctx, "kick_session", map[string]string{
		"user":     user,
		"host":     server,
		"resource": resource,
		"reason":   "Kicked by administrator",
	})
	return err
}

// KickUser disconnects all sessions for a user
func (a *Adapter) KickUser(ctx context.Context, username, domain string) error {
	_, err := a.doRequest(ctx, "kick_user", map[string]string{
		"user":   username,
		"host":   domain,
		"reason": "Kicked by administrator",
	})
	return err
}

// ListRooms lists all MUC rooms
func (a *Adapter) ListRooms(ctx context.Context, mucDomain string) ([]models.XMPPRoom, error) {
	resp, err := a.doRequest(ctx, "muc_online_rooms", map[string]string{
		"service": mucDomain,
	})
	if err != nil {
		return nil, err
	}

	// muc_online_rooms returns full "room@service" JIDs, not bare names, so
	// appending the domain again produced "room@svc@svc" and every options
	// lookup under that name failed.
	var roomJIDs []string
	if err := json.Unmarshal(resp, &roomJIDs); err != nil {
		return nil, fmt.Errorf("failed to parse rooms: %w", err)
	}

	rooms := make([]models.XMPPRoom, len(roomJIDs))
	for i, jid := range roomJIDs {
		name, service, isJID := strings.Cut(jid, "@")
		if !isJID {
			rooms[i] = models.XMPPRoom{JID: jid, Name: jid}
			continue
		}

		// Options and occupant count come from GetRoom, which asks per room.
		// One unreadable room degrades to name-only rather than failing the
		// whole listing.
		room, err := a.GetRoom(ctx, name, service)
		if err != nil {
			rooms[i] = models.XMPPRoom{JID: jid, Name: name}
			continue
		}
		rooms[i] = *room
	}

	return rooms, nil
}

// GetRoom retrieves information about a specific room
func (a *Adapter) GetRoom(ctx context.Context, room, mucDomain string) (*models.XMPPRoom, error) {
	resp, err := a.doRequest(ctx, "get_room_options", map[string]string{
		"name":    room,
		"service": mucDomain,
	})
	if err != nil {
		return nil, err
	}

	r := &models.XMPPRoom{
		JID:  fmt.Sprintf("%s@%s", room, mucDomain),
		Name: room,
	}

	var options []map[string]interface{}
	if json.Unmarshal(resp, &options) == nil {
		for _, opt := range options {
			if n, ok := opt["name"].(string); ok {
				if v, ok := opt["value"]; ok {
					switch n {
					case "title":
						r.Name = v.(string)
					case "description":
						r.Description = v.(string)
					case "public":
						r.Public = v == "true"
					case "persistent":
						r.Persistent = v == "true"
					case "members_only":
						r.MembersOnly = v == "true"
					}
				}
			}
		}
	}

	// Get occupants count
	if occResp, err := a.doRequest(ctx, "get_room_occupants_number", map[string]string{
		"name":    room,
		"service": mucDomain,
	}); err == nil {
		var count int
		if json.Unmarshal(occResp, &count) == nil {
			r.Occupants = count
		}
	}

	return r, nil
}

// CreateRoom creates a new MUC room
func (a *Adapter) CreateRoom(ctx context.Context, req models.CreateXMPPRoomRequest) error {
	// Create room
	_, err := a.doRequest(ctx, "create_room", map[string]string{
		"name":    req.Name,
		"service": req.Domain,
		"host":    req.Domain,
	})
	if err != nil {
		return err
	}

	// Set room options
	options := []map[string]string{
		{"name": "title", "value": req.Name},
		{"name": "description", "value": req.Description},
		{"name": "public", "value": fmt.Sprintf("%t", req.Public)},
		{"name": "persistent", "value": fmt.Sprintf("%t", req.Persistent)},
		{"name": "members_only", "value": fmt.Sprintf("%t", req.MembersOnly)},
	}

	for _, opt := range options {
		if _, err := a.doRequest(ctx, "change_room_option", map[string]string{
			"name":    req.Name,
			"service": req.Domain,
			"option":  opt["name"],
			"value":   opt["value"],
		}); err != nil {
			return fmt.Errorf("failed to set room option %s: %w", opt["name"], err)
		}
	}

	return nil
}

// DeleteRoom deletes a MUC room
func (a *Adapter) DeleteRoom(ctx context.Context, room, mucDomain string) error {
	_, err := a.doRequest(ctx, "destroy_room", map[string]string{
		"name":    room,
		"service": mucDomain,
	})
	return err
}

// ListModules lists all loaded modules
func (a *Adapter) ListModules(ctx context.Context) ([]types.ModuleInfo, error) {
	resp, err := a.doRequest(ctx, "loaded_modules", map[string]string{
		"host": a.server.Host,
	})
	if err != nil {
		return nil, err
	}

	var moduleNames []string
	if err := json.Unmarshal(resp, &moduleNames); err != nil {
		return nil, fmt.Errorf("failed to parse modules: %w", err)
	}

	modules := make([]types.ModuleInfo, len(moduleNames))
	for i, name := range moduleNames {
		modules[i] = types.ModuleInfo{
			Name:    name,
			Enabled: true,
		}
	}

	return modules, nil
}

// EnableModule enables a module
func (a *Adapter) EnableModule(ctx context.Context, module string) error {
	return apperrors.ErrNotImplemented
}

// DisableModule disables a module
func (a *Adapter) DisableModule(ctx context.Context, module string) error {
	return apperrors.ErrNotImplemented
}

// Capabilities reports what this adapter can do against ejabberd.
//
// NOTE: this adapter has not been validated against a real ejabberd
// server (CLAUDE.md flags this). The flags below describe what the code
// **attempts** to do, not what we've confirmed works. If you hit 502s on
// supposedly-supported endpoints, narrow these flags and patch the
// concrete methods.
func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		OnlineUsersCount:     true,  // connected_users_number
		RegisteredUsersCount: true,  // stats name=registeredusers
		ActiveSessionsCount:  false, // not populated by current GetStats
		S2SConnectionsCount:  true,  // incoming_s2s_number
		Sessions:             true,  // connected_users_info, kick_session/kick_user
		Rooms:                true,  // muc_online_rooms, get_room_options, etc
		Modules:              false, // EnableModule/DisableModule return ErrNotImplemented
	}
}

// doRequest performs an HTTP request to the ejabberd API
func (a *Adapter) doRequest(ctx context.Context, command string, args map[string]string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s", a.baseURL, command)

	var bodyReader io.Reader
	if args != nil {
		jsonBody, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
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
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return respBody, nil
	case http.StatusUnauthorized:
		return nil, apperrors.ErrAuthFailed
	case http.StatusNotFound:
		return nil, apperrors.ErrUserNotFound
	default:
		return nil, fmt.Errorf("%w: %s", apperrors.ErrOperationFailed, string(respBody))
	}
}

// Helper functions

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func parseJID(jid string) (user, server, resource string) {
	// Parse user@server/resource
	atPos := -1
	slashPos := -1

	for i, c := range jid {
		if c == '@' && atPos == -1 {
			atPos = i
		} else if c == '/' && slashPos == -1 {
			slashPos = i
		}
	}

	if atPos > 0 {
		user = jid[:atPos]
		if slashPos > atPos {
			server = jid[atPos+1 : slashPos]
			resource = jid[slashPos+1:]
		} else {
			server = jid[atPos+1:]
		}
	}

	return
}
