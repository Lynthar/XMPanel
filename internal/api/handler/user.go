package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/security/password"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

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

// List returns all users
func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, username, email, role, mfa_enabled, last_login_at, last_login_ip, created_at, updated_at
		FROM users ORDER BY created_at DESC
	`)
	if err != nil {
		h.logger.Error("failed to query users", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

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
		writeError(w, http.StatusBadRequest, "Invalid user ID")
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

	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}
	if err != nil {
		h.logger.Error("failed to query user", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// Create creates a new user
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate
	if len(req.Username) < 3 || len(req.Username) > 32 {
		writeError(w, http.StatusBadRequest, "Username must be 3-32 characters")
		return
	}
	if err := h.passwordValidator.Validate(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Check if username or email exists
	var exists int
	h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = $1 OR email = $2`, req.Username, req.Email).Scan(&exists)
	if exists > 0 {
		writeError(w, http.StatusConflict, "Username or email already exists")
		return
	}

	// Hash password
	passwordHash, err := h.hasher.Hash(req.Password)
	if err != nil {
		h.logger.Error("failed to hash password", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
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
		h.logger.Error("failed to create user", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	h.audit.LogEvent(r, models.AuditActionUserCreate, models.ResourceTypeUser, strconv.FormatInt(id, 10), "",
		map[string]interface{}{"username": req.Username, "role": string(req.Role)})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":      id,
		"message": "User created successfully",
	})
}

// Update updates a user
func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid user ID")
		return
	}

	var req models.UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Build update query
	updates := make(map[string]interface{})
	if req.Email != nil {
		updates["email"] = *req.Email
	}
	if req.Role != nil {
		updates["role"] = *req.Role
	}
	if req.Password != nil {
		if err := h.passwordValidator.Validate(*req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := h.hasher.Hash(*req.Password)
		if err != nil {
			h.logger.Error("failed to hash password", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		updates["password_hash"] = hash
	}

	if len(updates) == 0 {
		writeError(w, http.StatusBadRequest, "No fields to update")
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

	result, err := h.db.Exec(query, args...)
	if err != nil {
		h.logger.Error("failed to update user", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "User not found")
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

	writeJSON(w, http.StatusOK, map[string]string{"message": "User updated successfully"})
}

// Delete deletes a user
func (h *UserHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid user ID")
		return
	}

	// Don't allow deleting the last superadmin
	var superadminCount int
	h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'superadmin'`).Scan(&superadminCount)

	var userRole string
	h.db.QueryRow(`SELECT role FROM users WHERE id = $1`, id).Scan(&userRole)

	if userRole == string(models.RoleSuperAdmin) && superadminCount <= 1 {
		writeError(w, http.StatusForbidden, "Cannot delete the last superadmin")
		return
	}

	result, err := h.db.Exec(`DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		h.logger.Error("failed to delete user", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "User not found")
		return
	}

	h.audit.LogEvent(r, models.AuditActionUserDelete, models.ResourceTypeUser, idStr, "",
		map[string]interface{}{"role": userRole})

	writeJSON(w, http.StatusOK, map[string]string{"message": "User deleted successfully"})
}
