package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

const (
	backendListTimeout  = 30 * time.Second
	backendWriteTimeout = 10 * time.Second
)

// BackendHandler serves the protocol-neutral account, session and room routes.
// It only ever sees adapter.Adapter; nothing here knows which server it is.
type BackendHandler struct {
	adapters *registry.Registry
	audit    *AuditService
	logger   *zap.Logger
}

func NewBackendHandler(adapters *registry.Registry, audit *AuditService, logger *zap.Logger) *BackendHandler {
	return &BackendHandler{adapters: adapters, audit: audit, logger: logger}
}

// target resolves the server id in the path to its adapter, answering the
// request itself when that fails.
func (h *BackendHandler) target(w http.ResponseWriter, r *http.Request) (adapter.Adapter, int64, bool) {
	serverID, err := strconv.ParseInt(r.PathValue("serverId"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return nil, 0, false
	}
	a, _, err := h.adapters.Get(r.Context(), serverID)
	if err != nil {
		writeRegistryError(w, r, h.logger, err)
		return nil, 0, false
	}
	return a, serverID, true
}

// listQuery reads search/domain/limit/cursor, clamping limit into 1..MaxLimit.
func listQuery(r *http.Request) adapter.ListQuery {
	q := adapter.ListQuery{
		Search: r.URL.Query().Get("search"),
		Domain: r.URL.Query().Get("domain"),
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  adapter.DefaultLimit,
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		q.Limit = min(n, adapter.MaxLimit)
	}
	return q
}

func (h *BackendHandler) details(serverID int64, extra map[string]interface{}) map[string]interface{} {
	details := map[string]interface{}{"server_id": serverID}
	for k, v := range extra {
		details[k] = v
	}
	return details
}

func (h *BackendHandler) ListAccounts(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := a.ListAccounts(ctx, listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *BackendHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	account, err := a.GetAccount(ctx, r.PathValue("account"))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (h *BackendHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	var req adapter.CreateAccount
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Localpart == "" {
		writeError(w, r, http.StatusBadRequest, "Localpart is required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, r, http.StatusBadRequest, "Password must be at least 8 characters")
		return
	}
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	account, err := a.CreateAccount(ctx, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionAccountCreate, models.ResourceTypeAccount, account.ID, "",
		h.details(serverID, map[string]interface{}{"admin": req.Admin}))
	writeJSON(w, http.StatusCreated, account)
}

func (h *BackendHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.DeleteAccount(ctx, id); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionAccountDelete, models.ResourceTypeAccount, id, "", h.details(serverID, nil))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgAccountDeleted)})
}

func (h *BackendHandler) SetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, r, http.StatusBadRequest, "Password must be at least 8 characters")
		return
	}
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.SetPassword(ctx, id, req.Password); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionAccountPassword, models.ResourceTypeAccount, id, "", h.details(serverID, nil))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgAccountUpdated)})
}

func (h *BackendHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.SetEnabled(ctx, id, *req.Enabled); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionAccountEnabled, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"enabled": *req.Enabled}))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgAccountUpdated)})
}

func (h *BackendHandler) SetAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Admin *bool `json:"admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Admin == nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.SetAdmin(ctx, id, *req.Admin); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionAccountAdmin, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"admin": *req.Admin}))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgAccountUpdated)})
}

func (h *BackendHandler) ListAccountSessions(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	sessions, err := a.ListAccountSessions(ctx, r.PathValue("account"))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, sessions)
}

func (h *BackendHandler) TerminateAccountSessions(w http.ResponseWriter, r *http.Request) {
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.TerminateAccountSessions(ctx, id); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionSessionTerminate, models.ResourceTypeSession, id, "",
		h.details(serverID, map[string]interface{}{"scope": "account"}))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgSessionsTerminated)})
}

func (h *BackendHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := a.ListSessions(ctx, listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// TerminateSession takes the owning account from ?account= for backends
// whose session ids are only unique per account.
func (h *BackendHandler) TerminateSession(w http.ResponseWriter, r *http.Request) {
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("session")
	accountID := r.URL.Query().Get("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.TerminateSession(ctx, accountID, sessionID); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionSessionTerminate, models.ResourceTypeSession, sessionID, "",
		h.details(serverID, map[string]interface{}{"scope": "session", "account": accountID}))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgSessionTerminated)})
}

func (h *BackendHandler) ListRooms(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := a.ListRooms(ctx, listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *BackendHandler) GetRoom(w http.ResponseWriter, r *http.Request) {
	a, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	room, err := a.GetRoom(ctx, r.PathValue("room"))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, room)
}

func (h *BackendHandler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	var req adapter.CreateRoom
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "Room name is required")
		return
	}
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	room, err := a.CreateRoom(ctx, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionRoomCreate, models.ResourceTypeRoom, room.ID, "",
		h.details(serverID, map[string]interface{}{"public": req.Public, "persistent": req.Persistent, "members_only": req.MembersOnly}))
	writeJSON(w, http.StatusCreated, room)
}

func (h *BackendHandler) DeleteRoom(w http.ResponseWriter, r *http.Request) {
	a, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("room")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := a.DeleteRoom(ctx, id); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionRoomDelete, models.ResourceTypeRoom, id, "", h.details(serverID, nil))
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgRoomDeleted)})
}
