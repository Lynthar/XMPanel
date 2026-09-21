package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// ServerHandler handles XMPP server management endpoints
type ServerHandler struct {
	adapters *registry.Registry
	db       *store.DB
	keyRing  *crypto.KeyRing
	audit    *AuditService
	logger   *zap.Logger
}

// NewServerHandler creates a new server handler
func NewServerHandler(db *store.DB, keyRing *crypto.KeyRing, adapters *registry.Registry, audit *AuditService, logger *zap.Logger) *ServerHandler {
	return &ServerHandler{
		adapters: adapters,
		db:       db,
		keyRing:  keyRing,
		audit:    audit,
		logger:   logger,
	}
}

// List returns all XMPP servers
func (h *ServerHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, name, type, host, port, tls_enabled, enabled, created_at, updated_at
		FROM xmpp_servers ORDER BY name
	`)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query servers", err)
		return
	}
	defer func() { _ = rows.Close() }()

	servers := make([]models.XMPPServer, 0)
	for rows.Next() {
		var server models.XMPPServer
		err := rows.Scan(
			&server.ID, &server.Name, &server.Type, &server.Host, &server.Port,
			&server.TLSEnabled, &server.Enabled, &server.CreatedAt, &server.UpdatedAt,
		)
		if err != nil {
			h.logger.Error("failed to scan server", zap.Error(err))
			continue
		}
		servers = append(servers, server)
	}

	writeJSON(w, http.StatusOK, servers)
}

// Get returns a specific server
func (h *ServerHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	var server models.XMPPServer
	err = h.db.QueryRow(`
		SELECT id, name, type, host, port, tls_enabled, enabled, created_at, updated_at
		FROM xmpp_servers WHERE id = $1
	`, id).Scan(
		&server.ID, &server.Name, &server.Type, &server.Host, &server.Port,
		&server.TLSEnabled, &server.Enabled, &server.CreatedAt, &server.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query server", err)
		return
	}

	writeJSON(w, http.StatusOK, server)
}

// Create creates a new XMPP server
func (h *ServerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreateXMPPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate
	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "Name is required")
		return
	}
	if req.Host == "" {
		writeError(w, r, http.StatusBadRequest, "Host is required")
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		writeError(w, r, http.StatusBadRequest, "Invalid port")
		return
	}
	if !registry.Supports(req.Type) {
		writeError(w, r, http.StatusBadRequest, "Invalid server type")
		return
	}

	// Encrypt API key
	var encryptedAPIKey string
	if h.keyRing != nil && req.APIKey != "" {
		encrypted, err := h.keyRing.EncryptString(req.APIKey)
		if err != nil {
			writeInternalError(w, r, h.logger, "failed to encrypt API key", err)
			return
		}
		encryptedAPIKey = encrypted
	}

	// Insert server (PostgreSQL: use RETURNING since LastInsertId is unsupported)
	var id int64
	err := h.db.QueryRow(`
		INSERT INTO xmpp_servers (name, type, host, port, api_key_encrypted, tls_enabled, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, $8)
		RETURNING id
	`, req.Name, req.Type, req.Host, req.Port, encryptedAPIKey, req.TLSEnabled, time.Now(), time.Now()).Scan(&id)

	if err != nil {
		writeInternalError(w, r, h.logger, "failed to create server", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionServerAdd, models.ResourceTypeServer, strconv.FormatInt(id, 10), "",
		map[string]interface{}{"name": req.Name, "type": string(req.Type), "host": req.Host, "port": req.Port})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"message": "Server created successfully",
	})
}

// Update updates an XMPP server
func (h *ServerHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	var req models.UpdateXMPPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Build update query
	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.TLSEnabled != nil {
		updates["tls_enabled"] = *req.TLSEnabled
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.APIKey != nil && h.keyRing != nil {
		encrypted, err := h.keyRing.EncryptString(*req.APIKey)
		if err != nil {
			writeInternalError(w, r, h.logger, "failed to encrypt API key", err)
			return
		}
		updates["api_key_encrypted"] = encrypted
	}

	if len(updates) == 0 {
		writeError(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	updates["updated_at"] = time.Now()

	// Execute update with PostgreSQL numbered placeholders
	query := "UPDATE xmpp_servers SET "
	args := make([]interface{}, 0)
	paramNum := 1
	first := true
	for col, val := range updates {
		if !first {
			query += ", "
		}
		query += col + " = $" + strconv.Itoa(paramNum)
		args = append(args, val)
		paramNum++
		first = false
	}
	query += " WHERE id = $" + strconv.Itoa(paramNum)
	args = append(args, id)

	result, err := h.db.Exec(query, args...)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to update server", err)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}

	updatedFields := make([]string, 0, len(updates))
	for k := range updates {
		if k != "updated_at" {
			updatedFields = append(updatedFields, k)
		}
	}
	h.adapters.Invalidate(id)

	h.audit.LogEvent(r, models.AuditActionServerUpdate, models.ResourceTypeServer, idStr, "",
		map[string]interface{}{"fields": updatedFields})

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgServerUpdated)})
}

// Delete deletes an XMPP server
func (h *ServerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	result, err := h.db.Exec(`DELETE FROM xmpp_servers WHERE id = $1`, id)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to delete server", err)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}

	h.adapters.Invalidate(id)

	h.audit.LogEvent(r, models.AuditActionServerRemove, models.ResourceTypeServer, idStr, "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgServerDeleted)})
}

// Stats returns server statistics
func (h *ServerHandler) Stats(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	// Get server and create adapter
	xmppAdapter, err := h.adapters.Get(r.Context(), id)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stats, err := xmppAdapter.GetStats(ctx)
	if err != nil {
		writeAdapterError(w, r, h.logger, "server.stats", err)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

// Capabilities returns what features the configured server's adapter
// supports. Frontend uses this to hide tabs / stat tiles that would
// otherwise return 502.
func (h *ServerHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	xmppAdapter, err := h.adapters.Get(r.Context(), id)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	writeJSON(w, http.StatusOK, xmppAdapter.Capabilities())
}

// Test tests the connection to an XMPP server
func (h *ServerHandler) Test(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}

	xmppAdapter, err := h.adapters.Get(r.Context(), id)
	if err != nil {
		writeServerLookupError(w, r, h.logger, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	err = xmppAdapter.Ping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	// Get server info
	info, err := xmppAdapter.GetServerInfo(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Connection successful",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Connection successful",
		"info":    info,
	})
}
