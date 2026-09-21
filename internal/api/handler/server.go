package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

const serverColumns = `id, name, protocol, implementation, endpoint, domain, enabled, created_at, updated_at`

// ServerHandler manages the registry of backends the panel talks to.
type ServerHandler struct {
	adapters *registry.Registry
	db       *store.DB
	keyRing  *crypto.KeyRing
	audit    *AuditService
	logger   *zap.Logger
}

func NewServerHandler(db *store.DB, keyRing *crypto.KeyRing, adapters *registry.Registry, audit *AuditService, logger *zap.Logger) *ServerHandler {
	return &ServerHandler{adapters: adapters, db: db, keyRing: keyRing, audit: audit, logger: logger}
}

func scanServer(row interface{ Scan(...interface{}) error }) (models.Server, error) {
	var s models.Server
	err := row.Scan(&s.ID, &s.Name, &s.Protocol, &s.Implementation, &s.Endpoint, &s.Domain,
		&s.Enabled, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (h *ServerHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT ` + serverColumns + ` FROM servers ORDER BY name`)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query servers", err)
		return
	}
	defer func() { _ = rows.Close() }()

	servers := make([]models.Server, 0)
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			writeInternalError(w, r, h.logger, "failed to scan server", err)
			return
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		writeInternalError(w, r, h.logger, "failed to read servers", err)
		return
	}
	writeJSON(w, http.StatusOK, servers)
}

func (h *ServerHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	server, err := scanServer(h.db.QueryRow(`SELECT `+serverColumns+` FROM servers WHERE id = $1`, id))
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

// validEndpoint accepts an absolute http(s) URL with a host and no query.
func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return true
}

// credentialsOrError normalises a request's credentials: bearer needs a
// token; bearer+mas also needs the MAS endpoint, client id and secret.
func credentialsOrError(creds *adapter.Credentials) (adapter.Credentials, string) {
	if creds == nil {
		return adapter.Credentials{}, "Credentials are required"
	}
	if creds.Kind == "" {
		creds.Kind = adapter.CredentialsBearer
	}
	switch creds.Kind {
	case adapter.CredentialsBearer:
		creds.MAS = nil
	case adapter.CredentialsBearerMAS:
		if creds.MAS == nil || !validEndpoint(strings.TrimSpace(creds.MAS.Endpoint)) {
			return adapter.Credentials{}, "MAS endpoint must be an http or https URL"
		}
		if strings.TrimSpace(creds.MAS.ClientID) == "" || strings.TrimSpace(creds.MAS.ClientSecret) == "" {
			return adapter.Credentials{}, "MAS client id and client secret are required"
		}
		creds.MAS = &adapter.MASCredentials{
			Endpoint:     strings.TrimSpace(creds.MAS.Endpoint),
			ClientID:     strings.TrimSpace(creds.MAS.ClientID),
			ClientSecret: strings.TrimSpace(creds.MAS.ClientSecret),
		}
	default:
		return adapter.Credentials{}, "Unsupported credential kind"
	}
	if strings.TrimSpace(creds.Token) == "" {
		return adapter.Credentials{}, "Credential token is required"
	}
	return *creds, ""
}

func (h *ServerHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.Domain = strings.TrimSpace(req.Domain)
	switch {
	case req.Name == "":
		writeError(w, r, http.StatusBadRequest, "Name is required")
		return
	case !registry.Supports(req.Protocol, req.Implementation):
		writeError(w, r, http.StatusBadRequest, i18n.MsgUnsupportedServerType)
		return
	case !validEndpoint(req.Endpoint):
		writeError(w, r, http.StatusBadRequest, "Endpoint must be an http or https URL")
		return
	case req.Domain == "":
		writeError(w, r, http.StatusBadRequest, "Domain is required")
		return
	}
	creds, problem := credentialsOrError(req.Credentials)
	if problem != "" {
		writeError(w, r, http.StatusBadRequest, problem)
		return
	}
	if creds.MAS != nil && req.Implementation != adapter.ImplSynapse {
		writeError(w, r, http.StatusBadRequest, "MAS credentials apply to Synapse only")
		return
	}
	encrypted, err := store.EncryptCredentials(h.keyRing, creds)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to encrypt credentials", err)
		return
	}

	var id int64
	now := time.Now()
	err = h.db.QueryRow(`
		INSERT INTO servers (name, protocol, implementation, endpoint, domain, credentials_encrypted, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, $7)
		RETURNING id
	`, req.Name, req.Protocol, req.Implementation, req.Endpoint, req.Domain, encrypted, now).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, r, http.StatusConflict, "A server with this endpoint and domain already exists")
			return
		}
		writeInternalError(w, r, h.logger, "failed to create server", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionServerAdd, models.ResourceTypeServer, strconv.FormatInt(id, 10), "",
		map[string]interface{}{"name": req.Name, "protocol": req.Protocol, "implementation": req.Implementation,
			"endpoint": req.Endpoint, "domain": req.Domain})
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"message": middleware.T(r.Context(), i18n.MsgServerCreated),
	})
}

func (h *ServerHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	var req models.UpdateServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	var columns []string
	var args []interface{}
	set := func(column string, value interface{}) {
		args = append(args, value)
		columns = append(columns, column)
	}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			writeError(w, r, http.StatusBadRequest, "Name is required")
			return
		}
		set("name", strings.TrimSpace(*req.Name))
	}
	if req.Endpoint != nil {
		if !validEndpoint(strings.TrimSpace(*req.Endpoint)) {
			writeError(w, r, http.StatusBadRequest, "Endpoint must be an http or https URL")
			return
		}
		set("endpoint", strings.TrimSpace(*req.Endpoint))
	}
	if req.Domain != nil {
		if strings.TrimSpace(*req.Domain) == "" {
			writeError(w, r, http.StatusBadRequest, "Domain is required")
			return
		}
		set("domain", strings.TrimSpace(*req.Domain))
	}
	if req.Credentials != nil {
		creds, problem := credentialsOrError(req.Credentials)
		if problem != "" {
			writeError(w, r, http.StatusBadRequest, problem)
			return
		}
		if creds.MAS != nil {
			var impl adapter.Implementation
			switch err := h.db.QueryRow(`SELECT implementation FROM servers WHERE id = $1`, id).Scan(&impl); {
			case errors.Is(err, sql.ErrNoRows):
				writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
				return
			case err != nil:
				writeInternalError(w, r, h.logger, "failed to load server", err)
				return
			case impl != adapter.ImplSynapse:
				writeError(w, r, http.StatusBadRequest, "MAS credentials apply to Synapse only")
				return
			}
		}
		encrypted, err := store.EncryptCredentials(h.keyRing, creds)
		if err != nil {
			writeInternalError(w, r, h.logger, "failed to encrypt credentials", err)
			return
		}
		set("credentials_encrypted", encrypted)
	}
	if req.Enabled != nil {
		set("enabled", *req.Enabled)
	}
	if len(columns) == 0 {
		writeError(w, r, http.StatusBadRequest, "No fields to update")
		return
	}
	updated := append([]string(nil), columns...)
	set("updated_at", time.Now())
	assignments := make([]string, len(columns))
	for i, column := range columns {
		assignments[i] = column + " = $" + strconv.Itoa(i+1)
	}
	args = append(args, id)

	result, err := h.db.Exec(`UPDATE servers SET `+strings.Join(assignments, ", ")+` WHERE id = $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, r, http.StatusConflict, "A server with this endpoint and domain already exists")
			return
		}
		writeInternalError(w, r, h.logger, "failed to update server", err)
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	h.adapters.Invalidate(id)

	h.audit.LogEvent(r, models.AuditActionServerUpdate, models.ResourceTypeServer, idStr, "",
		map[string]interface{}{"fields": updated})
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgServerUpdated)})
}

func (h *ServerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	result, err := h.db.Exec(`DELETE FROM servers WHERE id = $1`, id)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to delete server", err)
		return
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	h.adapters.Invalidate(id)

	h.audit.LogEvent(r, models.AuditActionServerRemove, models.ResourceTypeServer, idStr, "", nil)
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgServerDeleted)})
}

func (h *ServerHandler) Stats(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	a, _, err := h.adapters.Get(r.Context(), id)
	if err != nil {
		writeRegistryError(w, r, h.logger, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	stats, err := a.Stats(ctx)
	if err != nil {
		writeAdapterError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// Capabilities returns the probed server facts and the operations the UI may
// offer; the registry answers from cache after the first probe.
func (h *ServerHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	a, info, err := h.adapters.Get(r.Context(), id)
	if err != nil {
		writeRegistryError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"info":         info,
		"capabilities": a.Capabilities(),
	})
}

// Test drops the cached adapter and probes again, so a corrected token or a
// newly installed module shows up immediately.
func (h *ServerHandler) Test(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid server ID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backendWriteTimeout)
	defer cancel()
	a, info, err := h.adapters.Reprobe(ctx, id)
	if err != nil {
		failure, ok := adapter.AsError(err)
		if !ok {
			writeRegistryError(w, r, h.logger, err)
			return
		}
		_, message := upstreamStatus(failure)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": false,
			"error":   middleware.T(r.Context(), message),
			"detail":  failure.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":      true,
		"message":      middleware.T(r.Context(), i18n.MsgServerTestSuccess),
		"info":         info,
		"capabilities": a.Capabilities(),
	})
}

// isUniqueViolation recognises PostgreSQL's 23505 without importing the
// driver's error type into the handler layer.
func isUniqueViolation(err error) bool {
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		return coded.SQLState() == "23505"
	}
	return false
}
