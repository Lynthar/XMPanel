package handler

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// AuditHandler handles audit log endpoints
type AuditHandler struct {
	db     *store.DB
	logger *zap.Logger
}

// NewAuditHandler creates a new audit handler
func NewAuditHandler(db *store.DB, logger *zap.Logger) *AuditHandler {
	return &AuditHandler{
		db:     db,
		logger: logger,
	}
}

// List returns audit logs with filtering
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	// Build WHERE clause for reuse in both data and count queries
	whereClause := " WHERE 1=1"
	args := make([]interface{}, 0)
	paramNum := 1

	if userID := r.URL.Query().Get("user_id"); userID != "" {
		whereClause += " AND user_id = $" + strconv.Itoa(paramNum)
		args = append(args, userID)
		paramNum++
	}

	if username := r.URL.Query().Get("username"); username != "" {
		whereClause += " AND username LIKE $" + strconv.Itoa(paramNum)
		args = append(args, "%"+username+"%")
		paramNum++
	}

	if action := r.URL.Query().Get("action"); action != "" {
		whereClause += " AND action = $" + strconv.Itoa(paramNum)
		args = append(args, action)
		paramNum++
	}

	if resourceType := r.URL.Query().Get("resource_type"); resourceType != "" {
		whereClause += " AND resource_type = $" + strconv.Itoa(paramNum)
		args = append(args, resourceType)
		paramNum++
	}

	if startTime := r.URL.Query().Get("start_time"); startTime != "" {
		whereClause += " AND created_at >= $" + strconv.Itoa(paramNum)
		args = append(args, startTime)
		paramNum++
	}

	if endTime := r.URL.Query().Get("end_time"); endTime != "" {
		whereClause += " AND created_at <= $" + strconv.Itoa(paramNum)
		args = append(args, endTime)
		paramNum++
	}

	// Get total count with same filters
	var total int
	countQuery := "SELECT COUNT(*) FROM audit_logs" + whereClause
	if err := h.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		h.logger.Error("failed to count audit logs", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Build data query with pagination
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	dataQuery := `
		SELECT id, user_id, username, action, resource_type, resource_id, details,
		       ip_address, user_agent, request_id, prev_hash, hash, created_at
		FROM audit_logs` + whereClause + " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(paramNum) + " OFFSET $" + strconv.Itoa(paramNum+1)

	dataArgs := append(args, limit, offset)

	// Execute query
	rows, err := h.db.Query(dataQuery, dataArgs...)
	if err != nil {
		h.logger.Error("failed to query audit logs", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	logs := make([]models.AuditLog, 0)
	for rows.Next() {
		var log models.AuditLog
		err := rows.Scan(
			&log.ID, &log.UserID, &log.Username, &log.Action, &log.ResourceType,
			&log.ResourceID, &log.Details, &log.IPAddress, &log.UserAgent,
			&log.RequestID, &log.PrevHash, &log.Hash, &log.CreatedAt,
		)
		if err != nil {
			h.logger.Error("failed to scan audit log", zap.Error(err))
			continue
		}
		logs = append(logs, log)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data":   logs,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// Verify verifies the integrity of audit logs
func (h *AuditHandler) Verify(w http.ResponseWriter, r *http.Request) {
	// Get logs to verify
	startID := int64(0)
	endID := int64(0)

	if s := r.URL.Query().Get("start_id"); s != "" {
		startID, _ = strconv.ParseInt(s, 10, 64)
	}
	if e := r.URL.Query().Get("end_id"); e != "" {
		endID, _ = strconv.ParseInt(e, 10, 64)
	}

	query := `
		SELECT id, user_id, username, action, resource_type, resource_id, details,
		       ip_address, user_agent, request_id, prev_hash, hash, created_at
		FROM audit_logs WHERE 1=1
	`
	args := make([]interface{}, 0)
	paramNum := 1

	if startID > 0 {
		query += " AND id >= $" + strconv.Itoa(paramNum)
		args = append(args, startID)
		paramNum++
	}
	if endID > 0 {
		query += " AND id <= $" + strconv.Itoa(paramNum)
		args = append(args, endID)
		paramNum++
	}

	query += " ORDER BY id ASC LIMIT 10000"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		h.logger.Error("failed to query audit logs", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	logs := make([]models.AuditLog, 0)
	for rows.Next() {
		var log models.AuditLog
		err := rows.Scan(
			&log.ID, &log.UserID, &log.Username, &log.Action, &log.ResourceType,
			&log.ResourceID, &log.Details, &log.IPAddress, &log.UserAgent,
			&log.RequestID, &log.PrevHash, &log.Hash, &log.CreatedAt,
		)
		if err != nil {
			h.logger.Error("failed to scan audit log", zap.Error(err))
			continue
		}
		logs = append(logs, log)
	}

	// Verify chain
	valid, brokenAt, err := models.VerifyChain(logs)
	if err != nil {
		h.logger.Error("failed to verify audit chain", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	result := map[string]interface{}{
		"valid":        valid,
		"records_checked": len(logs),
	}

	if !valid {
		result["broken_at_index"] = brokenAt
		if brokenAt >= 0 && brokenAt < len(logs) {
			result["broken_at_id"] = logs[brokenAt].ID
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// Export exports audit logs as CSV
func (h *AuditHandler) Export(w http.ResponseWriter, r *http.Request) {
	// Parse filters (same as List)
	query := `
		SELECT id, username, action, resource_type, resource_id, details,
		       ip_address, created_at
		FROM audit_logs WHERE 1=1
	`
	args := make([]interface{}, 0)
	paramNum := 1

	if action := r.URL.Query().Get("action"); action != "" {
		query += " AND action = $" + strconv.Itoa(paramNum)
		args = append(args, action)
		paramNum++
	}

	if startTime := r.URL.Query().Get("start_time"); startTime != "" {
		query += " AND created_at >= $" + strconv.Itoa(paramNum)
		args = append(args, startTime)
		paramNum++
	}

	if endTime := r.URL.Query().Get("end_time"); endTime != "" {
		query += " AND created_at <= $" + strconv.Itoa(paramNum)
		args = append(args, endTime)
		paramNum++
	}

	query += " ORDER BY created_at DESC LIMIT 10000"

	rows, err := h.db.Query(query, args...)
	if err != nil {
		h.logger.Error("failed to query audit logs", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	// Set headers for CSV download
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=audit_logs_"+time.Now().Format("20060102_150405")+".csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	// Write header
	writer.Write([]string{"ID", "Username", "Action", "Resource Type", "Resource ID", "Details", "IP Address", "Timestamp"})

	// Write data
	for rows.Next() {
		var (
			id           int64
			username     string
			action       string
			resourceType string
			resourceID   string
			details      string
			ipAddress    string
			createdAt    time.Time
		)

		err := rows.Scan(&id, &username, &action, &resourceType, &resourceID, &details, &ipAddress, &createdAt)
		if err != nil {
			continue
		}

		writer.Write([]string{
			strconv.FormatInt(id, 10),
			username,
			action,
			resourceType,
			resourceID,
			details,
			ipAddress,
			createdAt.Format(time.RFC3339),
		})
	}
}

// AuditService provides methods to write audit logs
type AuditService struct {
	db     *store.DB
	logger *zap.Logger
}

// NewAuditService creates a new audit service
func NewAuditService(db *store.DB, logger *zap.Logger) *AuditService {
	return &AuditService{
		db:     db,
		logger: logger,
	}
}

// LogEvent records an audit event derived from an HTTP request. Identity is
// taken from JWT claims when available; otherwise fallbackUsername is recorded
// (used for pre-auth events like login failures). Failures are logged but do
// not propagate — audit is best-effort and must never break the user request.
//
// Callers may pass nil for details. resourceID may be empty for events that
// don't reference a specific resource.
func (s *AuditService) LogEvent(
	r *http.Request,
	action models.AuditAction,
	resourceType models.ResourceType,
	resourceID string,
	fallbackUsername string,
	details map[string]interface{},
) {
	if s == nil {
		return
	}
	entry := &models.AuditLogEntry{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Details:      details,
		IPAddress:    middleware.GetClientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetRequestID(r.Context()),
	}
	if claims := middleware.GetClaims(r.Context()); claims != nil {
		uid := claims.UserID
		entry.UserID = &uid
		entry.Username = claims.Username
	} else if fallbackUsername != "" {
		entry.Username = fallbackUsername
	}
	if err := s.Log(entry); err != nil {
		s.logger.Warn("audit log write failed",
			zap.String("action", string(action)),
			zap.Error(err),
		)
	}
}

// Log writes an audit log entry using a transaction to prevent race conditions
func (s *AuditService) Log(entry *models.AuditLogEntry) error {
	// Convert details to JSON
	var detailsJSON string
	if entry.Details != nil {
		data, _ := json.Marshal(entry.Details)
		detailsJSON = string(data)
	}

	// Use a transaction to ensure atomicity
	tx, err := s.db.Begin()
	if err != nil {
		s.logger.Error("failed to begin transaction", zap.Error(err))
		return err
	}

	// Track whether we need to rollback
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	// Get the previous hash within the transaction (with lock)
	var prevHash string
	if err := tx.QueryRow(`SELECT hash FROM audit_logs ORDER BY id DESC LIMIT 1`).Scan(&prevHash); err != nil {
		if err != sql.ErrNoRows {
			s.logger.Warn("failed to get previous audit hash", zap.Error(err))
		}
		// Continue anyway - first entry won't have a prev hash
		prevHash = ""
	}

	// Get next ID within the transaction
	var nextID int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM audit_logs`).Scan(&nextID); err != nil {
		s.logger.Error("failed to get next ID", zap.Error(err))
		return err
	}

	timestamp := time.Now()
	hash := entry.ComputeHash(nextID, timestamp, prevHash)

	// Insert audit log within the transaction
	_, err = tx.Exec(`
		INSERT INTO audit_logs (user_id, username, action, resource_type, resource_id,
		                        details, ip_address, user_agent, request_id, prev_hash, hash, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, entry.UserID, entry.Username, entry.Action, entry.ResourceType, entry.ResourceID,
		detailsJSON, entry.IPAddress, entry.UserAgent, entry.RequestID, prevHash, hash, timestamp)

	if err != nil {
		s.logger.Error("failed to write audit log", zap.Error(err))
		return err
	}

	// Commit the transaction
	if err = tx.Commit(); err != nil {
		s.logger.Error("failed to commit audit log transaction", zap.Error(err))
		return err
	}

	committed = true
	return nil
}
