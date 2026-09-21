package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// MatrixHandler serves the moderation routes of adapter.MatrixAdmin. A
// server whose adapter lacks the extension answers 501 on every one of them.
type MatrixHandler struct {
	adapters *registry.Registry
	audit    *AuditService
	logger   *zap.Logger
}

func NewMatrixHandler(adapters *registry.Registry, audit *AuditService, logger *zap.Logger) *MatrixHandler {
	return &MatrixHandler{adapters: adapters, audit: audit, logger: logger}
}

func (h *MatrixHandler) target(w http.ResponseWriter, r *http.Request) (adapter.MatrixAdmin, int64, bool) {
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
	m, ok := a.(adapter.MatrixAdmin)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, i18n.MsgMatrixOnly)
		return nil, 0, false
	}
	return m, serverID, true
}

func (h *MatrixHandler) details(serverID int64, extra map[string]interface{}) map[string]interface{} {
	details := map[string]interface{}{"server_id": serverID, "protocol": string(adapter.ProtocolMatrix)}
	for k, v := range extra {
		details[k] = v
	}
	return details
}

func (h *MatrixHandler) message(w http.ResponseWriter, r *http.Request, key string) {
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), key)})
}

// Deactivate takes {"erase": bool}; erasing also removes message history
// and is the reason the route needs backend:danger.
func (h *MatrixHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Erase bool `json:"erase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.Deactivate(ctx, id, req.Erase); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixDeactivate, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"erase": req.Erase}))
	h.message(w, r, i18n.MsgAccountDeactivated)
}

func (h *MatrixHandler) SetSuspended(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Suspended *bool `json:"suspended"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Suspended == nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.SetSuspended(ctx, id, *req.Suspended); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixSuspend, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"suspended": *req.Suspended}))
	if *req.Suspended {
		h.message(w, r, i18n.MsgAccountSuspended)
	} else {
		h.message(w, r, i18n.MsgAccountUnsuspended)
	}
}

func (h *MatrixHandler) SetShadowBanned(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Banned *bool `json:"banned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Banned == nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.SetShadowBanned(ctx, id, *req.Banned); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixShadowBan, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"banned": *req.Banned}))
	if *req.Banned {
		h.message(w, r, i18n.MsgAccountShadowBanned)
	} else {
		h.message(w, r, i18n.MsgAccountUnshadowBanned)
	}
}

func (h *MatrixHandler) ListRegistrationTokens(w http.ResponseWriter, r *http.Request) {
	m, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	tokens, err := m.ListRegistrationTokens(ctx)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	if tokens == nil {
		tokens = []adapter.RegistrationToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (h *MatrixHandler) CreateRegistrationToken(w http.ResponseWriter, r *http.Request) {
	var req adapter.CreateRegistrationToken
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	// Zero means "unlimited" to Synapse and "unusable" to MAS, so it is
	// refused rather than mapped; omitting the field is the way to say unlimited.
	if req.UsesAllowed != nil && *req.UsesAllowed < 1 {
		writeError(w, r, http.StatusBadRequest, "Uses allowed must be positive; omit it for unlimited")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	token, err := m.CreateRegistrationToken(ctx, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	// The token itself is a credential and is not written to the audit log.
	extra := map[string]interface{}{"uses_allowed": req.UsesAllowed}
	if req.ExpiresAt != nil {
		extra["expires_at"] = req.ExpiresAt
	}
	h.audit.LogEvent(r, models.AuditActionMatrixRegTokenCreate, models.ResourceTypeToken, adapter.MaskToken(token.Token), "", h.details(serverID, extra))
	writeJSON(w, http.StatusCreated, token)
}

func (h *MatrixHandler) DeleteRegistrationToken(w http.ResponseWriter, r *http.Request) {
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	token := r.PathValue("token")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.DeleteRegistrationToken(ctx, token); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixRegTokenDelete, models.ResourceTypeToken, adapter.MaskToken(token), "", h.details(serverID, nil))
	h.message(w, r, i18n.MsgRegTokenDeleted)
}

func (h *MatrixHandler) ListReports(w http.ResponseWriter, r *http.Request) {
	m, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := m.ListReports(ctx, listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *MatrixHandler) ListAccountMedia(w http.ResponseWriter, r *http.Request) {
	m, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := m.ListAccountMedia(ctx, r.PathValue("account"), listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *MatrixHandler) QuarantineAccountMedia(w http.ResponseWriter, r *http.Request) {
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("account")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	n, err := m.QuarantineAccountMedia(ctx, id)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixMediaQuarantine, models.ResourceTypeAccount, id, "",
		h.details(serverID, map[string]interface{}{"quarantined": n}))
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": middleware.T(r.Context(), i18n.MsgMediaQuarantined), "quarantined": n})
}

func (h *MatrixHandler) DeleteMedia(w http.ResponseWriter, r *http.Request) {
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	mediaID := r.PathValue("mediaId")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.DeleteMedia(ctx, mediaID); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixMediaDelete, models.ResourceTypeMedia, mediaID, "", h.details(serverID, nil))
	h.message(w, r, i18n.MsgMediaDeleted)
}

func (h *MatrixHandler) BlockRoom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Block *bool `json:"block"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Block == nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("room")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.BlockRoom(ctx, id, *req.Block); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixRoomBlock, models.ResourceTypeRoom, id, "",
		h.details(serverID, map[string]interface{}{"block": *req.Block}))
	if *req.Block {
		h.message(w, r, i18n.MsgRoomBlocked)
	} else {
		h.message(w, r, i18n.MsgRoomUnblocked)
	}
}

// PurgeRoom starts the shutdown and answers its task id; the room is gone
// only when Synapse finishes the task.
func (h *MatrixHandler) PurgeRoom(w http.ResponseWriter, r *http.Request) {
	var req adapter.PurgeRoom
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	id := r.PathValue("room")
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	deleteID, err := m.PurgeRoom(ctx, id, req)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixRoomPurge, models.ResourceTypeRoom, id, "",
		h.details(serverID, map[string]interface{}{"purge": req.Purge, "block": req.Block, "force_purge": req.ForcePurge, "new_room_user": req.NewRoomUser, "delete_id": deleteID}))
	writeJSON(w, http.StatusAccepted, map[string]string{"message": middleware.T(r.Context(), i18n.MsgRoomPurgeStarted), "delete_id": deleteID})
}

func (h *MatrixHandler) SendNotice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Account string `json:"account"`
		Body    string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.Account == "" || strings.TrimSpace(req.Body) == "" {
		writeError(w, r, http.StatusBadRequest, "Account and body are required")
		return
	}
	m, serverID, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	if err := m.SendServerNotice(ctx, req.Account, req.Body); err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	h.audit.LogEvent(r, models.AuditActionMatrixNotice, models.ResourceTypeAccount, req.Account, "",
		h.details(serverID, map[string]interface{}{"length": len(req.Body)}))
	h.message(w, r, i18n.MsgNoticeSent)
}

func (h *MatrixHandler) ListFederation(w http.ResponseWriter, r *http.Request) {
	m, _, ok := h.target(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendListTimeout)
	defer cancel()
	page, err := m.ListFederationDestinations(ctx, listQuery(r))
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
