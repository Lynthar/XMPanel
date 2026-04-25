package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/auth"
	"github.com/xmpanel/xmpanel/internal/i18n"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/security/password"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/models"

	"go.uber.org/zap"
)

// AuthHandler handles authentication endpoints
type AuthHandler struct {
	db                *store.DB
	jwtManager        *auth.JWTManager
	hasher            *crypto.Argon2Hasher
	passwordValidator *password.Validator
	totpManager       *auth.TOTPManager
	loginLimiter      *middleware.LoginRateLimiter
	audit             *AuditService
	logger            *zap.Logger
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler(
	db *store.DB,
	jwtManager *auth.JWTManager,
	hasher *crypto.Argon2Hasher,
	passwordValidator *password.Validator,
	loginLimiter *middleware.LoginRateLimiter,
	audit *AuditService,
	logger *zap.Logger,
) *AuthHandler {
	return &AuthHandler{
		db:                db,
		jwtManager:        jwtManager,
		hasher:            hasher,
		passwordValidator: passwordValidator,
		totpManager:       auth.NewTOTPManager("XMPanel"),
		loginLimiter:      loginLimiter,
		audit:             audit,
		logger:            logger,
	}
}

// Login handles user login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())

	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgBadRequest)
		return
	}

	// Check rate limit (use IP without port — RemoteAddr includes ephemeral port)
	clientIP := middleware.GetClientIP(r)
	allowed, lockDuration := h.loginLimiter.Check(clientIP + ":" + req.Username)
	if !allowed {
		w.Header().Set("Retry-After", lockDuration.String())
		writeErrorI18n(w, http.StatusTooManyRequests, locale, i18n.MsgRateLimitExceeded)
		return
	}

	// Get user from database
	var user models.User
	err := h.db.QueryRow(`
		SELECT id, username, email, password_hash, role, mfa_enabled, mfa_secret,
		       failed_login_attempts, locked_until, last_login_at, last_login_ip,
		       created_at, updated_at
		FROM users WHERE username = $1
	`, req.Username).Scan(
		&user.ID, &user.Username, &user.Email, &user.PasswordHash, &user.Role,
		&user.MFAEnabled, &user.MFASecret, &user.FailedLoginAttempts, &user.LockedUntil,
		&user.LastLoginAt, &user.LastLoginIP, &user.CreatedAt, &user.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, "", req.Username,
			map[string]interface{}{"reason": "user_not_found"})
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgInvalidCredentials)
		return
	}
	if err != nil {
		h.logger.Error("failed to query user", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Check if account is locked
	if user.IsLocked() {
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
			map[string]interface{}{"reason": "account_locked"})
		writeErrorI18n(w, http.StatusForbidden, locale, i18n.MsgAccountLocked)
		return
	}

	// Verify password
	valid, err := h.hasher.Verify(req.Password, user.PasswordHash)
	if err != nil || !valid {
		// Record failed attempt
		h.db.Exec(`
			UPDATE users SET failed_login_attempts = failed_login_attempts + 1,
			       locked_until = CASE WHEN failed_login_attempts >= 4 THEN NOW() + INTERVAL '15 minutes' ELSE locked_until END
			WHERE id = $1
		`, user.ID)
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
			map[string]interface{}{"reason": "invalid_password"})
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgInvalidCredentials)
		return
	}

	// Check MFA if enabled
	if user.MFAEnabled {
		if req.TOTPCode == "" {
			// MFA required but not provided
			writeJSON(w, http.StatusOK, models.LoginResponse{
				MFARequired: true,
			})
			return
		}

		// Verify TOTP code
		if user.MFASecret.Valid {
			valid, err := h.totpManager.ValidateCode(user.MFASecret.String, req.TOTPCode)
			if err != nil || !valid {
				h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
					map[string]interface{}{"reason": "mfa_invalid"})
				writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgMFAInvalid)
				return
			}
		}
	}

	// Generate session ID
	sessionID, err := crypto.GenerateRandomString(32)
	if err != nil {
		h.logger.Error("failed to generate session ID", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Generate tokens
	tokenPair, err := h.jwtManager.GenerateTokenPair(
		user.ID,
		user.Username,
		string(user.Role),
		sessionID,
		"", // Device ID (optional)
	)
	if err != nil {
		h.logger.Error("failed to generate tokens", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Create session record
	_, err = h.db.Exec(`
		INSERT INTO sessions (user_id, session_id, ip_address, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
	`, user.ID, sessionID, clientIP, r.UserAgent(), tokenPair.ExpiresAt.Add(7*24*time.Hour))
	if err != nil {
		h.logger.Error("failed to create session", zap.Error(err))
	}

	// Update login info and reset failed attempts
	h.db.Exec(`
		UPDATE users SET last_login_at = $1, last_login_ip = $2, failed_login_attempts = 0, locked_until = NULL
		WHERE id = $3
	`, time.Now(), clientIP, user.ID)

	// Clear rate limiter on success
	h.loginLimiter.RecordSuccess(clientIP + ":" + req.Username)

	// Audit successful login (claims aren't set on login request, so pass username explicitly)
	h.audit.LogEvent(r, models.AuditActionLogin, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
		map[string]interface{}{"session_id": sessionID})

	// Return response
	user.PasswordHash = ""
	user.MFASecret = sql.NullString{}
	user.RecoveryCodes = sql.NullString{}

	writeJSON(w, http.StatusOK, models.LoginResponse{
		AccessToken:  tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresAt:    tokenPair.ExpiresAt,
		TokenType:    tokenPair.TokenType,
		User:         &user,
	})
}

// Refresh handles token refresh
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())

	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgBadRequest)
		return
	}

	// First validate the refresh token
	claims, err := h.jwtManager.ValidateToken(req.RefreshToken, auth.TokenTypeRefresh)
	if err != nil {
		switch err {
		case auth.ErrExpiredToken:
			writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgTokenExpired)
		default:
			writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgTokenInvalid)
		}
		return
	}

	// Check if session still exists in database (hasn't been revoked)
	var exists int
	err = h.db.QueryRow(`SELECT 1 FROM sessions WHERE session_id = $1 AND user_id = $2`,
		claims.SessionID, claims.UserID).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			// Session has been revoked (user logged out or password changed)
			writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgSessionRevoked)
			return
		}
		h.logger.Error("failed to check session", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Check if user account is still valid
	var userRole string
	var lockedUntil sql.NullTime
	err = h.db.QueryRow(`SELECT role, locked_until FROM users WHERE id = $1`, claims.UserID).
		Scan(&userRole, &lockedUntil)
	if err != nil {
		if err == sql.ErrNoRows {
			writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUserNotFound)
			return
		}
		h.logger.Error("failed to check user", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Check if account is locked
	if lockedUntil.Valid && lockedUntil.Time.After(time.Now()) {
		writeErrorI18n(w, http.StatusForbidden, locale, i18n.MsgAccountLocked)
		return
	}

	// Generate new token pair with current role (in case it changed)
	tokenPair, err := h.jwtManager.GenerateTokenPair(
		claims.UserID,
		claims.Username,
		userRole, // Use current role from DB
		claims.SessionID,
		claims.DeviceID,
	)
	if err != nil {
		h.logger.Error("failed to generate tokens", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Update session expiry
	h.db.Exec(`UPDATE sessions SET expires_at = $1 WHERE session_id = $2`,
		tokenPair.ExpiresAt.Add(7*24*time.Hour), claims.SessionID)

	writeJSON(w, http.StatusOK, tokenPair)
}

// Logout handles user logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	// Delete session
	_, err := h.db.Exec(`DELETE FROM sessions WHERE session_id = $1`, claims.SessionID)
	if err != nil {
		h.logger.Error("failed to delete session", zap.Error(err))
	}

	h.audit.LogEvent(r, models.AuditActionLogout, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "",
		map[string]interface{}{"session_id": claims.SessionID})

	writeJSON(w, http.StatusOK, map[string]string{"message": i18n.T(locale, i18n.MsgLogoutSuccess)})
}

// Me returns the current user info
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	var user models.User
	err := h.db.QueryRow(`
		SELECT id, username, email, role, mfa_enabled, last_login_at, last_login_ip, created_at, updated_at
		FROM users WHERE id = $1
	`, claims.UserID).Scan(
		&user.ID, &user.Username, &user.Email, &user.Role, &user.MFAEnabled,
		&user.LastLoginAt, &user.LastLoginIP, &user.CreatedAt, &user.UpdatedAt,
	)

	if err != nil {
		writeErrorI18n(w, http.StatusNotFound, locale, i18n.MsgUserNotFound)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// SetupMFA initiates MFA setup
func (h *AuthHandler) SetupMFA(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	// Generate TOTP secret
	secret, err := h.totpManager.GenerateSecret(claims.Username)
	if err != nil {
		h.logger.Error("failed to generate TOTP secret", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Store secret temporarily (not enabled yet)
	_, err = h.db.Exec(`UPDATE users SET mfa_secret = $1 WHERE id = $2`, secret.Secret, claims.UserID)
	if err != nil {
		h.logger.Error("failed to store MFA secret", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	writeJSON(w, http.StatusOK, secret)
}

// VerifyMFA verifies and enables MFA
func (h *AuthHandler) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgBadRequest)
		return
	}

	// Get user's MFA secret
	var secret sql.NullString
	err := h.db.QueryRow(`SELECT mfa_secret FROM users WHERE id = $1`, claims.UserID).Scan(&secret)
	if err != nil || !secret.Valid {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgMFANotEnabled)
		return
	}

	// Verify code
	valid, err := h.totpManager.ValidateCode(secret.String, req.Code)
	if err != nil || !valid {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgMFAInvalid)
		return
	}

	// Enable MFA
	_, err = h.db.Exec(`UPDATE users SET mfa_enabled = TRUE WHERE id = $1`, claims.UserID)
	if err != nil {
		h.logger.Error("failed to enable MFA", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Generate recovery codes
	recoveryManager := auth.NewRecoveryCodeManager()
	codes, err := recoveryManager.GenerateCodes()
	if err != nil {
		h.logger.Error("failed to generate recovery codes", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Hash and store recovery codes
	hashedCodes, err := recoveryManager.HashCodes(codes, h.hasher)
	if err != nil {
		h.logger.Error("failed to hash recovery codes", zap.Error(err))
	} else {
		codesJSON, _ := json.Marshal(hashedCodes)
		h.db.Exec(`UPDATE users SET recovery_codes = $1 WHERE id = $2`, string(codesJSON), claims.UserID)
	}

	h.audit.LogEvent(r, models.AuditActionMFAEnabled, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":        i18n.T(locale, i18n.MsgMFASetupSuccess),
		"recovery_codes": codes,
	})
}

// DisableMFA disables MFA for the current user
func (h *AuthHandler) DisableMFA(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgBadRequest)
		return
	}

	// Verify password
	var passwordHash string
	var mfaSecret sql.NullString
	err := h.db.QueryRow(`SELECT password_hash, mfa_secret FROM users WHERE id = $1`, claims.UserID).
		Scan(&passwordHash, &mfaSecret)
	if err != nil {
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	valid, err := h.hasher.Verify(req.Password, passwordHash)
	if err != nil || !valid {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgPasswordMismatch)
		return
	}

	// Verify TOTP code
	if mfaSecret.Valid {
		valid, err := h.totpManager.ValidateCode(mfaSecret.String, req.Code)
		if err != nil || !valid {
			writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgMFAInvalid)
			return
		}
	}

	// Disable MFA
	_, err = h.db.Exec(`UPDATE users SET mfa_enabled = FALSE, mfa_secret = NULL, recovery_codes = NULL WHERE id = $1`, claims.UserID)
	if err != nil {
		h.logger.Error("failed to disable MFA", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	h.audit.LogEvent(r, models.AuditActionMFADisabled, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": i18n.T(locale, i18n.MsgMFADisabled)})
}

// ChangePassword handles password change
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	locale := middleware.GetLocale(r.Context())
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgBadRequest)
		return
	}

	// Validate new password against policy
	if err := h.passwordValidator.Validate(req.NewPassword); err != nil {
		writeErrorI18n(w, http.StatusBadRequest, locale, i18n.MsgPasswordWeak)
		return
	}

	// Get current password hash
	var currentHash string
	err := h.db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, claims.UserID).Scan(&currentHash)
	if err != nil {
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Verify current password
	valid, err := h.hasher.Verify(req.CurrentPassword, currentHash)
	if err != nil || !valid {
		writeErrorI18n(w, http.StatusUnauthorized, locale, i18n.MsgPasswordMismatch)
		return
	}

	// Hash new password
	newHash, err := h.hasher.Hash(req.NewPassword)
	if err != nil {
		h.logger.Error("failed to hash password", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Update password
	_, err = h.db.Exec(`UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`,
		newHash, time.Now(), claims.UserID)
	if err != nil {
		h.logger.Error("failed to update password", zap.Error(err))
		writeErrorI18n(w, http.StatusInternalServerError, locale, i18n.MsgInternalError)
		return
	}

	// Invalidate all other sessions
	_, err = h.db.Exec(`DELETE FROM sessions WHERE user_id = $1 AND session_id != $2`,
		claims.UserID, claims.SessionID)
	if err != nil {
		h.logger.Error("failed to invalidate sessions", zap.Error(err))
	}

	h.audit.LogEvent(r, models.AuditActionPasswordChange, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": i18n.T(locale, i18n.MsgPasswordChanged)})
}

// Helper functions

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeErrorI18n(w http.ResponseWriter, status int, locale i18n.Locale, msgKey string) {
	writeJSON(w, status, map[string]string{"error": i18n.T(locale, msgKey)})
}
