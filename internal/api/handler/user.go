package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/security/password"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// Outcomes of the two role rules below. errUnknownRole maps to 400 (the client
// sent something meaningless), errNeedSuperAdmin to 403.
var (
	errUnknownRole    = errors.New("unknown role")
	errNeedSuperAdmin = errors.New("superadmin required")
)

// checkRoleGrant reports whether caller may store requested as someone's role.
// Only a superadmin hands out superadmin — the route group admits admins too,
// so without this an admin can promote any account, itself included.
func checkRoleGrant(caller, requested models.Role) error {
	if !requested.IsValid() {
		return errUnknownRole
	}
	if requested == models.RoleSuperAdmin && caller != models.RoleSuperAdmin {
		return errNeedSuperAdmin
	}
	return nil
}

// checkTargetWritable reports whether caller may update or delete an account
// that currently holds target. A superadmin account is off limits to everyone
// below it, otherwise an admin takes it over by resetting its password.
func checkTargetWritable(caller, target models.Role) error {
	if target == models.RoleSuperAdmin && caller != models.RoleSuperAdmin {
		return errNeedSuperAdmin
	}
	return nil
}

// callerRole returns the authenticated caller's role, or false when the
// request carries no claims (the route group should have rejected it).
func callerRole(r *http.Request) (models.Role, bool) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		return "", false
	}
	return models.Role(claims.Role), true
}

// writeRoleError maps a checkRoleGrant / checkTargetWritable result to a status.
func writeRoleError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errUnknownRole) {
		writeError(w, r, http.StatusBadRequest, "Unknown role")
		return
	}
	writeError(w, r, http.StatusForbidden, "Only a superadmin can do that")
}

// UserHandler handles user management endpoints
type UserHandler struct {
	db                *store.DB
	hasher            *crypto.Argon2Hasher
	keyRing           *crypto.KeyRing
	passwordValidator *password.Validator
	audit             *AuditService
	logger            *zap.Logger
}

// NewUserHandler creates a new user handler
func NewUserHandler(db *store.DB, hasher *crypto.Argon2Hasher, keyRing *crypto.KeyRing, passwordValidator *password.Validator, audit *AuditService, logger *zap.Logger) *UserHandler {
	return &UserHandler{
		db:                db,
		hasher:            hasher,
		keyRing:           keyRing,
		passwordValidator: passwordValidator,
		audit:             audit,
		logger:            logger,
	}
}

// roleOf reads one account's stored role. Returns sql.ErrNoRows when the id
// does not exist, so callers can answer 404 instead of silently treating a
// missing row as an unprivileged one.
func (h *UserHandler) roleOf(id int64) (models.Role, error) {
	var role models.Role
	err := h.db.QueryRow(`SELECT role FROM users WHERE id = $1`, id).Scan(&role)
	return role, err
}

// List returns all users
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, username, email, role, mfa_enabled, last_login_at, last_login_ip, created_at, updated_at
		FROM users ORDER BY created_at DESC
	`)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query users", err)
		return
	}
	defer func() { _ = rows.Close() }()

	users := make([]models.User, 0)
	for rows.Next() {
		var user models.User
		err := rows.Scan(
			&user.ID, &user.Username, &user.Email, &user.Role, &user.MFAEnabled,
			&user.LastLoginAt, &user.LastLoginIP, &user.CreatedAt, &user.UpdatedAt,
		)
		if err != nil {
			h.logger.Error("failed to scan user", zap.Error(err))
			continue
		}
		users = append(users, user)
	}

	writeJSON(w, http.StatusOK, users)
}

// Get returns a specific user
func (h *UserHandler) Get(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid user ID")
		return
	}

	var user models.User
	err = h.db.QueryRow(`
		SELECT id, username, email, role, mfa_enabled, last_login_at, last_login_ip, created_at, updated_at
		FROM users WHERE id = $1
	`, id).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.MFAEnabled,
		&user.LastLoginAt, &user.LastLoginIP, &user.CreatedAt, &user.UpdatedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query user", err)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// Create creates a new user
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	caller, ok := callerRole(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	// Validate
	if len(req.Username) < 3 || len(req.Username) > 32 {
		writeError(w, r, http.StatusBadRequest, "Username must be 3-32 characters")
		return
	}
	if err := checkRoleGrant(caller, req.Role); err != nil {
		writeRoleError(w, r, err)
		return
	}
	if err := h.passwordValidator.Validate(req.Password); err != nil {
		writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Check if username or email exists
	var exists int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = $1 OR email = $2`, req.Username, req.Email).Scan(&exists); err != nil {
		writeInternalError(w, r, h.logger, "failed to check for existing user", err)
		return
	}
	if exists > 0 {
		writeError(w, r, http.StatusConflict, i18n.MsgUserAlreadyExists)
		return
	}

	// Hash password
	passwordHash, err := h.hasher.Hash(req.Password)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to hash password", err)
		return
	}

	// Insert user (PostgreSQL: use RETURNING since LastInsertId is unsupported)
	var id int64
	err = h.db.QueryRow(`
		INSERT INTO users (username, email, password_hash, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, req.Username, req.Email, passwordHash, req.Role, time.Now(), time.Now()).Scan(&id)

	if err != nil {
		writeInternalError(w, r, h.logger, "failed to create user", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionUserCreate, models.ResourceTypeUser, strconv.FormatInt(id, 10), "",
		map[string]interface{}{"username": req.Username, "role": string(req.Role)})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"message": middleware.T(r.Context(), i18n.MsgUserCreated),
	})
}

// Update updates a user
func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid user ID")
		return
	}

	var req models.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	caller, ok := callerRole(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	targetRole, err := h.roleOf(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to read target user role", err)
		return
	}
	if err := checkTargetWritable(caller, targetRole); err != nil {
		writeRoleError(w, r, err)
		return
	}

	// Build update query
	updates := make(map[string]interface{})
	if req.Email != nil {
		updates["email"] = *req.Email
	}
	if req.Role != nil {
		if err := checkRoleGrant(caller, *req.Role); err != nil {
			writeRoleError(w, r, err)
			return
		}
		updates["role"] = *req.Role
	}
	if req.Password != nil {
		if err := h.passwordValidator.Validate(*req.Password); err != nil {
			writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := h.hasher.Hash(*req.Password)
		if err != nil {
			writeInternalError(w, r, h.logger, "failed to hash password", err)
			return
		}
		updates["password_hash"] = hash
	}

	if len(updates) == 0 {
		writeError(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	updates["updated_at"] = time.Now()

	// Execute update with PostgreSQL numbered placeholders
	query := "UPDATE users SET "
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

	// The password write and the session purge have to land together: a new
	// password with the old refresh tokens still valid leaves whoever the reset
	// was aimed at logged in for the full refresh lifetime.
	tx, err := h.db.Begin()
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to begin user update", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit has run

	result, err := tx.Exec(query, args...)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to update user", err)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}

	if _, ok := updates["password_hash"]; ok {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
			writeInternalError(w, r, h.logger, "failed to revoke sessions after password reset", err)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeInternalError(w, r, h.logger, "failed to commit user update", err)
		return
	}

	updatedFields := make([]string, 0, len(updates))
	for k := range updates {
		if k != "updated_at" {
			updatedFields = append(updatedFields, k)
		}
	}
	h.audit.LogEvent(r, models.AuditActionUserUpdate, models.ResourceTypeUser, idStr, "",
		map[string]interface{}{"fields": updatedFields})

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgUserUpdated)})
}

// Delete deletes a user
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "Invalid user ID")
		return
	}

	caller, ok := callerRole(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	userRole, err := h.roleOf(id)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to read target user role", err)
		return
	}
	if err := checkTargetWritable(caller, userRole); err != nil {
		writeRoleError(w, r, err)
		return
	}

	// Don't allow deleting the last superadmin
	if userRole == models.RoleSuperAdmin {
		var superadminCount int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'superadmin'`).Scan(&superadminCount); err != nil {
			writeInternalError(w, r, h.logger, "failed to count superadmins", err)
			return
		}
		if superadminCount <= 1 {
			writeError(w, r, http.StatusForbidden, "Cannot delete the last superadmin")
			return
		}
	}

	result, err := h.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to delete user", err)
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}

	h.audit.LogEvent(r, models.AuditActionUserDelete, models.ResourceTypeUser, idStr, "",
		map[string]interface{}{"role": userRole})

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgUserDeleted)})
}
