package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// ServerHandler handles XMPP server management endpoints
type ServerHandler struct {
	db      *store.DB
	keyRing *crypto.KeyRing
	audit   *AuditService
	logger  *zap.Logger
}

// NewServerHandler creates a new server handler
func NewServerHandler(db *store.DB, keyRing *crypto.KeyRing, audit *AuditService, logger *zap.Logger) *ServerHandler {
	return &ServerHandler{
		db:      db,
		keyRing: keyRing,
		audit:   audit,
		logger:  logger,
	}
}

// List returns all XMPP servers
func (h *ServerHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, name, type, host, port, tls_enabled, enabled, created_at, updated_at
		FROM xmpp_servers ORDER BY name
	`)
	if err != nil {
		h.logger.Error("failed to query servers", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

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
		writeError(w, http.StatusBadRequest, "Invalid server ID")
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

	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "Server not found")
		return
	}
	if err != nil {
		h.logger.Error("failed to query server", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, server)
}

// Create creates a new XMPP server
func (h *ServerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreateXMPPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	if req.Host == "" {
		writeError(w, http.StatusBadRequest, "Host is required")
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		writeError(w, http.StatusBadRequest, "Invalid port")
		return
	}
	if req.Type != models.ServerTypeProsody && req.Type != models.ServerTypeEjabberd {
		writeError(w, http.StatusBadRequest, "Invalid server type")
		return
	}

	// Encrypt API key
	var encryptedAPIKey string
	if h.keyRing != nil && req.APIKey != "" {
		encrypted, err := h.keyRing.EncryptString(req.APIKey)
		if err != nil {
			h.logger.Error("failed to encrypt API key", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
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
		h.logger.Error("failed to create server", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
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
		writeError(w, http.StatusBadRequest, "Invalid server ID")
		return
	}

	var req models.UpdateXMPPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
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
			h.logger.Error("failed to encrypt API key", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		updates["api_key_encrypted"] = encrypted
	}

	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, "No fields to update")
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
		h.logger.Error("failed to update server", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "Server not found")
		return
	}

	updatedFields := make([]string, 0, len(updates))
	for k := range updates {
		if k != "updated_at" {
			updatedFields = append(updatedFields, k)
		}
	}
	h.audit.LogEvent(r, models.AuditActionServerUpdate, models.ResourceTypeServer, idStr, "",
		map[string]interface{}{"fields": updatedFields})

	writeJSON(w, http.StatusOK, map[string]string{"message": "Server updated successfully"})
}

// Delete deletes an XMPP server
func (h *ServerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid server ID")
		return
	}

	result, err := h.db.Exec(`DELETE FROM xmpp_servers WHERE id = $1`, id)
	if err != nil {
		h.logger.Error("failed to delete server", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "Server not found")
		return
	}

	h.audit.LogEvent(r, models.AuditActionServerRemove, models.ResourceTypeServer, idStr, "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": "Server deleted successfully"})
}

// Stats returns server statistics
func (h *ServerHandler) Stats(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid server ID")
		return
	}

	// Get server and create adapter
	xmppAdapter, err := h.getAdapter(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "Server not found")
		} else {
			h.logger.Error("failed to get adapter", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
		}
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	stats, err := xmppAdapter.GetStats(ctx)
	if err != nil {
		h.logger.Error("failed to get server stats", zap.Error(err))
		writeError(w, http.StatusBadGateway, "Failed to get server statistics")
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
		writeError(w, http.StatusBadRequest, "Invalid server ID")
		return
	}

	xmppAdapter, err := h.getAdapter(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "Server not found")
		} else {
			h.logger.Error("failed to get adapter", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
		}
		return
	}

	writeJSON(w, http.StatusOK, xmppAdapter.Capabilities())
}

// Test tests the connection to an XMPP server
func (h *ServerHandler) Test(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid server ID")
		return
	}

	xmppAdapter, err := h.getAdapter(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "Server not found")
		} else {
			h.logger.Error("failed to get adapter", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
		}
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

// getAdapter creates an adapter for the given server ID
func (h *ServerHandler) getAdapter(serverID int64) (adapter.XMPPAdapter, error) {
	return GetXMPPAdapter(h.db, h.keyRing, serverID)
}
