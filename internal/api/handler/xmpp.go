package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// XMPPHandler handles XMPP operations endpoints
type XMPPHandler struct {
	adapters *registry.Registry
	audit    *AuditService
	logger   *zap.Logger
}

// NewXMPPHandler creates a new XMPP handler
func NewXMPPHandler(adapters *registry.Registry, audit *AuditService, logger *zap.Logger) *XMPPHandler {
	return &XMPPHandler{
		adapters: adapters,
		audit:    audit,
		logger:   logger,
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	users, err := xmppAdapter.ListUsers(ctx, domain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "accounts.list", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	user, err := xmppAdapter.GetUser(ctx, username, domain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "accounts.get", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.CreateUser(ctx, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, "accounts.create", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.DeleteUser(ctx, username, domain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "accounts.delete", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.KickUser(ctx, username, domain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "sessions.terminate_all", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	sessions, err := xmppAdapter.GetOnlineSessions(ctx)
	if err != nil {
		writeAdapterError(w, r, h.logger, "sessions.list", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.KickSession(ctx, jid)
	if err != nil {
		writeAdapterError(w, r, h.logger, "sessions.terminate", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rooms, err := xmppAdapter.ListRooms(ctx, mucDomain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "rooms.list", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	roomInfo, err := xmppAdapter.GetRoom(ctx, room, mucDomain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "rooms.get", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.CreateRoom(ctx, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, "rooms.create", err)
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

	xmppAdapter, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.DeleteRoom(ctx, room, mucDomain)
	if err != nil {
		writeAdapterError(w, r, h.logger, "rooms.delete", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionXMPPRoomDelete, models.ResourceTypeRoom, room+"@"+mucDomain, "",
		map[string]interface{}{"server_id": serverID})

	writeJSON(w, http.StatusOK, map[string]string{"message": "Room deleted successfully"})
}
