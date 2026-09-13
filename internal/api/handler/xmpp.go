package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// XMPPHandler handles XMPP operations endpoints
type XMPPHandler struct {
	db      *store.DB
	keyRing *crypto.KeyRing
	audit   *AuditService
	logger  *zap.Logger
}

// NewXMPPHandler creates a new XMPP handler
func NewXMPPHandler(db *store.DB, keyRing *crypto.KeyRing, audit *AuditService, logger *zap.Logger) *XMPPHandler {
	return &XMPPHandler{
		db:      db,
		keyRing: keyRing,
		audit:   audit,
		logger:  logger,
	}
}

// ListUsers lists all users on an XMPP server
func (h *XMPPHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "Domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	users, err := xmppAdapter.ListUsers(ctx, domain)
	if err != nil {
		writeUpstreamError(w, r, h.logger, "Failed to list users", err)
		return
	}

	writeJSON(w, http.StatusOK, users)
}

// GetUser gets a specific user on an XMPP server
func (h *XMPPHandler) GetUser(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	username := r.PathValue("username")
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "Domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	user, err := xmppAdapter.GetUser(ctx, username, domain)
	if err != nil {
		if err == adapter.ErrUserNotFound {
			writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to get user", err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// CreateUser creates a new user on an XMPP server
func (h *XMPPHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	var req models.CreateXMPPUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate
	if req.Username == "" {
		writeError(w, r, http.StatusBadRequest, "Username is required")
		return
	}
	if req.Domain == "" {
		writeError(w, r, http.StatusBadRequest, "Domain is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, r, http.StatusBadRequest, "Password must be at least 8 characters")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.CreateUser(ctx, req)
	if err != nil {
		if err == adapter.ErrUserExists {
			writeError(w, r, http.StatusConflict, "User already exists")
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to create user", err)
		return
	}

	jid := req.Username + "@" + req.Domain
	h.audit.LogEvent(r, models.AuditActionXMPPUserCreate, models.ResourceTypeXMPP, jid, "",
		map[string]interface{}{"server_id": serverID, "username": req.Username, "domain": req.Domain})

	writeJSON(w, http.StatusCreated, map[string]string{
		"message": middleware.T(r.Context(), i18n.MsgUserCreated),
		"jid":     jid,
	})
}

// DeleteUser deletes a user from an XMPP server
func (h *XMPPHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	username := r.PathValue("username")
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "Domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.DeleteUser(ctx, username, domain)
	if err != nil {
		if err == adapter.ErrUserNotFound {
			writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to delete user", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionXMPPUserDelete, models.ResourceTypeXMPP, username+"@"+domain, "",
		map[string]interface{}{"server_id": serverID})

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgUserDeleted)})
}

// KickUser kicks a user from an XMPP server
func (h *XMPPHandler) KickUser(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	username := r.PathValue("username")
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "Domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.KickUser(ctx, username, domain)
	if err != nil {
		writeUpstreamError(w, r, h.logger, "Failed to kick user", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionXMPPUserKick, models.ResourceTypeXMPP, username+"@"+domain, "",
		map[string]interface{}{"server_id": serverID, "scope": "all_sessions"})

	writeJSON(w, http.StatusOK, map[string]string{"message": "User kicked successfully"})
}

// ListSessions lists all online sessions
func (h *XMPPHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	sessions, err := xmppAdapter.GetOnlineSessions(ctx)
	if err != nil {
		writeUpstreamError(w, r, h.logger, "Failed to list sessions", err)
		return
	}

	writeJSON(w, http.StatusOK, sessions)
}

// KickSession kicks a specific session
func (h *XMPPHandler) KickSession(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	jid := r.PathValue("jid")

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.KickSession(ctx, jid)
	if err != nil {
		writeUpstreamError(w, r, h.logger, "Failed to kick session", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionXMPPUserKick, models.ResourceTypeXMPP, jid, "",
		map[string]interface{}{"server_id": serverID, "scope": "single_session"})

	writeJSON(w, http.StatusOK, map[string]string{"message": "Session kicked successfully"})
}

// ListRooms lists all MUC rooms
func (h *XMPPHandler) ListRooms(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	mucDomain := r.URL.Query().Get("muc_domain")
	if mucDomain == "" {
		writeError(w, r, http.StatusBadRequest, "MUC domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rooms, err := xmppAdapter.ListRooms(ctx, mucDomain)
	if err != nil {
		writeUpstreamError(w, r, h.logger, "Failed to list rooms", err)
		return
	}

	writeJSON(w, http.StatusOK, rooms)
}

// GetRoom gets a specific room
func (h *XMPPHandler) GetRoom(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	room := r.PathValue("room")
	mucDomain := r.URL.Query().Get("muc_domain")
	if mucDomain == "" {
		writeError(w, r, http.StatusBadRequest, "MUC domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	roomInfo, err := xmppAdapter.GetRoom(ctx, room, mucDomain)
	if err != nil {
		if err == adapter.ErrRoomNotFound {
			writeError(w, r, http.StatusNotFound, "Room not found")
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to get room", err)
		return
	}

	writeJSON(w, http.StatusOK, roomInfo)
}

// CreateRoom creates a new MUC room
func (h *XMPPHandler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	var req models.CreateXMPPRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate
	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "Room name is required")
		return
	}
	if req.Domain == "" {
		writeError(w, r, http.StatusBadRequest, "MUC domain is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.CreateRoom(ctx, req)
	if err != nil {
		if err == adapter.ErrRoomExists {
			writeError(w, r, http.StatusConflict, "Room already exists")
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to create room", err)
		return
	}

	jid := req.Name + "@" + req.Domain
	h.audit.LogEvent(r, models.AuditActionXMPPRoomCreate, models.ResourceTypeRoom, jid, "",
		map[string]interface{}{"server_id": serverID, "public": req.Public, "persistent": req.Persistent, "members_only": req.MembersOnly})

	writeJSON(w, http.StatusCreated, map[string]string{
		"message": "Room created successfully",
		"jid":     jid,
	})
}

// DeleteRoom deletes a MUC room
func (h *XMPPHandler) DeleteRoom(w http.ResponseWriter, r *http.Request) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	room := r.PathValue("room")
	mucDomain := r.URL.Query().Get("muc_domain")
	if mucDomain == "" {
		writeError(w, r, http.StatusBadRequest, "MUC domain parameter is required")
		return
	}

	xmppAdapter, err := GetXMPPAdapter(h.db, h.keyRing, serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.DeleteRoom(ctx, room, mucDomain)
	if err != nil {
		if err == adapter.ErrRoomNotFound {
			writeError(w, r, http.StatusNotFound, "Room not found")
			return
		}
		writeUpstreamError(w, r, h.logger, "Failed to delete room", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionXMPPRoomDelete, models.ResourceTypeRoom, room+"@"+mucDomain, "",
		map[string]interface{}{"server_id": serverID})

	writeJSON(w, http.StatusOK, map[string]string{"message": "Room deleted successfully"})
}
