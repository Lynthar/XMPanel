package models

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// AuditAction represents the type of auditable action
type AuditAction string

const (
	// Authentication actions
	AuditActionLogin          AuditAction = "auth.login"
	AuditActionLoginFailed    AuditAction = "auth.login_failed"
	AuditActionLogout         AuditAction = "auth.logout"
	AuditActionMFAEnabled     AuditAction = "auth.mfa_enabled"
	AuditActionMFADisabled    AuditAction = "auth.mfa_disabled"
	AuditActionPasswordChange AuditAction = "auth.password_change"

	// User management actions
	AuditActionUserCreate AuditAction = "user.create"
	AuditActionUserUpdate AuditAction = "user.update"
	AuditActionUserDelete AuditAction = "user.delete"

	// Server management actions
	AuditActionServerAdd    AuditAction = "server.add"
	AuditActionServerUpdate AuditAction = "server.update"
	AuditActionServerRemove AuditAction = "server.remove"

	// XMPP operations
	AuditActionXMPPUserCreate AuditAction = "xmpp.user_create"
	AuditActionXMPPUserDelete AuditAction = "xmpp.user_delete"
	AuditActionXMPPUserKick   AuditAction = "xmpp.user_kick"
	AuditActionXMPPRoomCreate AuditAction = "xmpp.room_create"
	AuditActionXMPPRoomDelete AuditAction = "xmpp.room_delete"

	// System actions
	AuditActionSettingChange AuditAction = "system.setting_change"
)

// ResourceType represents the type of resource being audited
type ResourceType string

const (
	ResourceTypeUser    ResourceType = "user"
	ResourceTypeServer  ResourceType = "server"
	ResourceTypeXMPP    ResourceType = "xmpp"
	ResourceTypeRoom    ResourceType = "room"
	ResourceTypeSetting ResourceType = "setting"
)

// AuditLog represents an audit log entry.
//
// JSON marshaling unwraps sql.Null* fields into bare strings/numbers (or
// null) so frontend code can render them directly. Without this, the
// default encoding would emit objects like {"String":"...","Valid":true}
// which React refuses to render as a child node (error #31).
type AuditLog struct {
	ID           int64          `json:"id" db:"id"`
	UserID       sql.NullInt64  `json:"-" db:"user_id"`
	Username     string         `json:"username" db:"username"`
	Action       AuditAction    `json:"action" db:"action"`
	ResourceType ResourceType   `json:"resource_type,omitempty" db:"resource_type"`
	ResourceID   sql.NullString `json:"-" db:"resource_id"`
	Details      sql.NullString `json:"-" db:"details"`
	IPAddress    sql.NullString `json:"-" db:"ip_address"`
	UserAgent    sql.NullString `json:"-" db:"user_agent"`
	RequestID    sql.NullString `json:"-" db:"request_id"`
	PrevHash     sql.NullString `json:"-" db:"prev_hash"`
	Hash         string         `json:"hash" db:"hash"`
	CreatedAt    time.Time      `json:"created_at" db:"created_at"`
}

// auditLogJSON is the wire format for AuditLog. Pointer-to-string/int64
// fields encode as the bare value when set, or null when not.
type auditLogJSON struct {
	ID           int64           `json:"id"`
	UserID       *int64          `json:"user_id,omitempty"`
	Username     string          `json:"username"`
	Action       AuditAction     `json:"action"`
	ResourceType ResourceType    `json:"resource_type,omitempty"`
	ResourceID   *string         `json:"resource_id,omitempty"`
	Details      json.RawMessage `json:"details,omitempty"`
	IPAddress    *string         `json:"ip_address,omitempty"`
	UserAgent    *string         `json:"user_agent,omitempty"`
	RequestID    *string         `json:"request_id,omitempty"`
	PrevHash     *string         `json:"prev_hash,omitempty"`
	Hash         string          `json:"hash"`
	CreatedAt    time.Time       `json:"created_at"`
}

func nullStringPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullInt64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// MarshalJSON unwraps the sql.Null* fields. Details, when present, is
// emitted as a JSON value (not a JSON-encoded string) since the column
// is JSONB.
func (l AuditLog) MarshalJSON() ([]byte, error) {
	out := auditLogJSON{
		ID:           l.ID,
		UserID:       nullInt64Ptr(l.UserID),
		Username:     l.Username,
		Action:       l.Action,
		ResourceType: l.ResourceType,
		ResourceID:   nullStringPtr(l.ResourceID),
		IPAddress:    nullStringPtr(l.IPAddress),
		UserAgent:    nullStringPtr(l.UserAgent),
		RequestID:    nullStringPtr(l.RequestID),
		PrevHash:     nullStringPtr(l.PrevHash),
		Hash:         l.Hash,
		CreatedAt:    l.CreatedAt,
	}
	if l.Details.Valid && l.Details.String != "" {
		out.Details = json.RawMessage(l.Details.String)
	}
	return json.Marshal(out)
}

// AuditLogEntry is used for creating new audit log entries
type AuditLogEntry struct {
	UserID       *int64                 `json:"user_id,omitempty"`
	Username     string                 `json:"username"`
	Action       AuditAction            `json:"action"`
	ResourceType ResourceType           `json:"resource_type,omitempty"`
	ResourceID   string                 `json:"resource_id,omitempty"`
	Details      map[string]interface{} `json:"details,omitempty"`
	IPAddress    string                 `json:"ip_address,omitempty"`
	UserAgent    string                 `json:"user_agent,omitempty"`
	RequestID    string                 `json:"request_id,omitempty"`
}

// ComputeHash computes the hash for an audit log entry
func (e *AuditLogEntry) ComputeHash(id int64, timestamp time.Time, prevHash string) string {
	detailsJSON, _ := json.Marshal(e.Details)

	data := fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s",
		id,
		timestamp.UTC().Format(time.RFC3339Nano),
		e.Action,
		e.Username,
		e.ResourceType,
		e.ResourceID,
		string(detailsJSON),
		e.IPAddress,
		e.UserAgent,
		e.RequestID,
		prevHash,
	)

	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// VerifyChain verifies the integrity of a chain of audit logs
func VerifyChain(logs []AuditLog) (bool, int, error) {
	if len(logs) == 0 {
		return true, 0, nil
	}

	for i, log := range logs {
		entry := &AuditLogEntry{
			Username:     log.Username,
			Action:       log.Action,
			ResourceType: log.ResourceType,
			IPAddress:    log.IPAddress.String,
			UserAgent:    log.UserAgent.String,
			RequestID:    log.RequestID.String,
		}

		if log.ResourceID.Valid {
			entry.ResourceID = log.ResourceID.String
		}

		if log.Details.Valid {
			json.Unmarshal([]byte(log.Details.String), &entry.Details)
		}

		prevHash := ""
		if log.PrevHash.Valid {
			prevHash = log.PrevHash.String
		}

		expectedHash := entry.ComputeHash(log.ID, log.CreatedAt, prevHash)
		if expectedHash != log.Hash {
			return false, i, nil
		}
	}

	return true, -1, nil
}

// AuditLogFilter represents filters for querying audit logs
type AuditLogFilter struct {
	UserID       *int64       `json:"user_id,omitempty"`
	Username     string       `json:"username,omitempty"`
	Action       AuditAction  `json:"action,omitempty"`
	ResourceType ResourceType `json:"resource_type,omitempty"`
	ResourceID   string       `json:"resource_id,omitempty"`
	StartTime    *time.Time   `json:"start_time,omitempty"`
	EndTime      *time.Time   `json:"end_time,omitempty"`
	Limit        int          `json:"limit,omitempty"`
	Offset       int          `json:"offset,omitempty"`
}
