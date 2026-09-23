package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// Cookie names for authentication. The refresh cookie is HttpOnly so JS can't
// read it. The CSRF cookie is intentionally readable so the SPA can mirror its
// value into the X-CSRF-Token header (double-submit pattern).
const (
	refreshCookieName = "xmpanel_refresh"
	csrfCookieName    = "csrf_token"
	authCookiePath    = "/api/v1/auth"
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
	refreshTTL        time.Duration
	secureCookies     bool
	logger            *zap.Logger
}

// NewAuthHandler creates a new auth handler.
// secureCookies should be true when the deployment terminates TLS (so cookies
// get the Secure attribute). refreshTTL controls cookie Max-Age and matches
// the refresh JWT lifetime.
func NewAuthHandler(
	db *store.DB,
	jwtManager *auth.JWTManager,
	hasher *crypto.Argon2Hasher,
	passwordValidator *password.Validator,
	loginLimiter *middleware.LoginRateLimiter,
	audit *AuditService,
	refreshTTL time.Duration,
	secureCookies bool,
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
		refreshTTL:        refreshTTL,
		secureCookies:     secureCookies,
		logger:            logger,
	}
}

// setRefreshCookie writes the refresh JWT as an HttpOnly cookie scoped to the
// auth endpoints, so the browser ships it on /auth/refresh and /auth/logout
// but never on other API calls.
func (h *AuthHandler) setRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    value,
		Path:     authCookiePath,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.refreshTTL.Seconds()),
	})
}

// setCSRFCookie writes a fresh random token as a non-HttpOnly cookie. The SPA
// reads it via document.cookie and mirrors the value to X-CSRF-Token; the CSRF
// middleware on /auth/refresh checks header == cookie.
func (h *AuthHandler) setCSRFCookie(w http.ResponseWriter) error {
	token, err := crypto.GenerateRandomString(32)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.refreshTTL.Seconds()),
	})
	return nil
}

// clearAuthCookies expires both auth cookies. Browsers delete cookies when a
// matching name+path is sent with MaxAge<=0.
func (h *AuthHandler) clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Path:     authCookiePath,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Path:     "/",
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// Login handles user login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {

	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, i18n.MsgBadRequest)
		return
	}

	// Check rate limit (use IP without port — RemoteAddr includes ephemeral port)
	clientIP := middleware.GetClientIP(r)
	allowed, lockDuration := h.loginLimiter.Check(clientIP + ":" + req.Username)
	if !allowed {
		w.Header().Set("Retry-After", lockDuration.String())
		writeError(w, r, http.StatusTooManyRequests, i18n.MsgRateLimitExceeded)
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

	if errors.Is(err, sql.ErrNoRows) {
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, "", req.Username,
			map[string]interface{}{"reason": "user_not_found"})
		writeError(w, r, http.StatusUnauthorized, i18n.MsgInvalidCredentials)
		return
	}
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query user", err)
		return
	}

	// Check if account is locked
	if user.IsLocked() {
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
			map[string]interface{}{"reason": "account_locked"})
		writeError(w, r, http.StatusForbidden, i18n.MsgAccountLocked)
		return
	}

	// Verify password
	valid, err := h.hasher.Verify(req.Password, user.PasswordHash)
	if err != nil || !valid {
		// Record failed attempt
		if _, err := h.db.Exec(`
			UPDATE users SET failed_login_attempts = failed_login_attempts + 1,
			       locked_until = CASE WHEN failed_login_attempts >= 4 THEN NOW() + INTERVAL '15 minutes' ELSE locked_until END
			WHERE id = $1
		`, user.ID); err != nil {
			h.logger.Error("failed to record failed login attempt", zap.Error(err))
		}
		h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
			map[string]interface{}{"reason": "invalid_password"})
		writeError(w, r, http.StatusUnauthorized, i18n.MsgInvalidCredentials)
		return
	}

	// Check MFA if enabled. Caller may respond to the mfa_required prompt with
	// either a TOTP code or a one-time recovery code (mutually exclusive).
	if user.MFAEnabled {
		bothEmpty := req.TOTPCode == "" && req.RecoveryCode == ""
		bothSet := req.TOTPCode != "" && req.RecoveryCode != ""

		if bothEmpty {
			writeJSON(w, http.StatusOK, models.LoginResponse{MFARequired: true})
			return
		}
		if bothSet {
			writeError(w, r, http.StatusBadRequest, i18n.MsgBadRequest)
			return
		}

		if req.RecoveryCode != "" {
			ok, err := h.verifyAndConsumeRecoveryCode(user.ID, req.RecoveryCode)
			if err != nil {
				writeInternalError(w, r, h.logger, "failed to verify recovery code", err)
				return
			}
			if !ok {
				h.audit.LogEvent(r, models.AuditActionRecoveryLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
					map[string]interface{}{"reason": "recovery_code_invalid"})
				writeError(w, r, http.StatusUnauthorized, i18n.MsgRecoveryCodeInvalid)
				return
			}
			// Successful recovery login — record separately from regular login
			// so audit reviewers can spot accounts that have fallen back to
			// recovery codes (often a signal the operator lost their authenticator).
			h.audit.LogEvent(r, models.AuditActionRecoveryLogin, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username, nil)
		} else if user.MFASecret.Valid {
			valid, err := h.totpManager.ValidateCode(user.MFASecret.String, req.TOTPCode)
			if err != nil || !valid {
				h.audit.LogEvent(r, models.AuditActionLoginFailed, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
					map[string]interface{}{"reason": "mfa_invalid"})
				writeError(w, r, http.StatusUnauthorized, i18n.MsgMFAInvalid)
				return
			}
		}
	}

	// Generate session ID
	sessionID, err := crypto.GenerateRandomString(32)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to generate session ID", err)
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
		writeInternalError(w, r, h.logger, "failed to generate tokens", err)
		return
	}

	// Create session record. Store a hash of the refresh token so /auth/refresh
	// can detect token reuse — old refresh tokens become invalid after rotation.
	refreshHash := crypto.HashToken(tokenPair.RefreshToken)
	_, err = h.db.Exec(`
		INSERT INTO sessions (user_id, session_id, refresh_token_hash, ip_address, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, user.ID, sessionID, refreshHash, clientIP, r.UserAgent(), tokenPair.ExpiresAt.Add(7*24*time.Hour))
	if err != nil {
		h.logger.Error("failed to create session", zap.Error(err))
	}

	// Update login info and reset failed attempts
	if _, err := h.db.Exec(`
		UPDATE users SET last_login_at = $1, last_login_ip = $2, failed_login_attempts = 0, locked_until = NULL
		WHERE id = $3
	`, time.Now(), clientIP, user.ID); err != nil {
		h.logger.Error("failed to update login info", zap.Error(err))
	}

	// Clear rate limiter on success
	h.loginLimiter.RecordSuccess(clientIP + ":" + req.Username)

	// Audit successful login (claims aren't set on login request, so pass username explicitly)
	h.audit.LogEvent(r, models.AuditActionLogin, models.ResourceTypeUser, strconv.FormatInt(user.ID, 10), user.Username,
		map[string]interface{}{"session_id": sessionID})

	// Set the refresh JWT as an HttpOnly cookie (out-of-band) and a paired CSRF
	// token in a JS-readable cookie. The SPA never sees the refresh token.
	h.setRefreshCookie(w, tokenPair.RefreshToken)
	if err := h.setCSRFCookie(w); err != nil {
		h.logger.Error("failed to set CSRF cookie", zap.Error(err))
	}

	// Return response
	user.PasswordHash = ""
	user.MFASecret = sql.NullString{}
	user.RecoveryCodes = sql.NullString{}

	writeJSON(w, http.StatusOK, models.LoginResponse{
		AccessToken: tokenPair.AccessToken,
		ExpiresAt:   tokenPair.ExpiresAt,
		TokenType:   tokenPair.TokenType,
		User:        &user,
	})
}

// Refresh handles token refresh. The refresh JWT is delivered via the
// xmpanel_refresh HttpOnly cookie (the request body is ignored) and is
// rotated on every call.
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {

	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenInvalid)
		return
	}
	refreshToken := cookie.Value

	// First validate the refresh token
	claims, err := h.jwtManager.ValidateToken(refreshToken, auth.TokenTypeRefresh)
	if err != nil {
		switch err {
		case auth.ErrExpiredToken:
			writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenExpired)
		default:
			writeError(w, r, http.StatusUnauthorized, i18n.MsgTokenInvalid)
		}
		return
	}

	// Look up session and current refresh-token hash. Session row gone =
	// session was revoked (logout, password change). Hash mismatch = token
	// reuse — refresh tokens are single-use; the previous /auth/refresh has
	// already rotated this session's hash. Treat reuse as a possible theft and
	// kill the entire session.
	var storedHash sql.NullString
	err = h.db.QueryRow(`SELECT refresh_token_hash FROM sessions WHERE session_id = $1 AND user_id = $2`,
		claims.SessionID, claims.UserID).Scan(&storedHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusUnauthorized, i18n.MsgSessionRevoked)
			return
		}
		writeInternalError(w, r, h.logger, "failed to check session", err)
		return
	}

	incomingHash := crypto.HashToken(refreshToken)
	if !storedHash.Valid || storedHash.String != incomingHash {
		h.revokeReplayedSession(w, r, claims)
		return
	}

	// Check if user account is still valid
	var userRole string
	var lockedUntil sql.NullTime
	err = h.db.QueryRow(`SELECT role, locked_until FROM users WHERE id = $1`, claims.UserID).
		Scan(&userRole, &lockedUntil)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusUnauthorized, i18n.MsgUserNotFound)
			return
		}
		writeInternalError(w, r, h.logger, "failed to check user", err)
		return
	}

	// Check if account is locked
	if lockedUntil.Valid && lockedUntil.Time.After(time.Now()) {
		writeError(w, r, http.StatusForbidden, i18n.MsgAccountLocked)
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
		writeInternalError(w, r, h.logger, "failed to generate tokens", err)
		return
	}

	// Rotate only if the hash is still the one checked above: two concurrent
	// calls with one token must not both succeed, or the cookie the browser
	// keeps and the hash stored here can end up belonging to different tokens.
	newHash := crypto.HashToken(tokenPair.RefreshToken)
	res, err := h.db.Exec(`UPDATE sessions SET refresh_token_hash = $1, expires_at = $2, last_used_at = NOW()
		WHERE session_id = $3 AND refresh_token_hash = $4`,
		newHash, tokenPair.ExpiresAt.Add(7*24*time.Hour), claims.SessionID, incomingHash)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to rotate refresh token", err)
		return
	}
	rotated, err := res.RowsAffected()
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to rotate refresh token", err)
		return
	}
	if rotated == 0 {
		h.revokeReplayedSession(w, r, claims)
		return
	}

	// Re-issue cookies (refresh JWT rotates; CSRF token rotates with it).
	h.setRefreshCookie(w, tokenPair.RefreshToken)
	if err := h.setCSRFCookie(w); err != nil {
		h.logger.Error("failed to set CSRF cookie", zap.Error(err))
	}

	// Only the access token (and metadata) leaves via JSON.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": tokenPair.AccessToken,
		"expires_at":   tokenPair.ExpiresAt,
		"token_type":   tokenPair.TokenType,
	})
}

// revokeReplayedSession answers a refresh token presented after it was
// rotated away: possible theft, so the whole session goes, and the cookies
// are cleared so the client falls back to /login instead of looping.
func (h *AuthHandler) revokeReplayedSession(w http.ResponseWriter, r *http.Request, claims *auth.Claims) {
	if _, err := h.db.Exec(`DELETE FROM sessions WHERE session_id = $1`, claims.SessionID); err != nil {
		h.logger.Error("failed to revoke session", zap.Error(err))
	}
	h.logger.Warn("refresh token reuse detected, revoking session",
		zap.Int64("user_id", claims.UserID),
		zap.String("session_id", claims.SessionID))
	h.clearAuthCookies(w)
	writeError(w, r, http.StatusUnauthorized, i18n.MsgSessionRevoked)
}

// Logout handles user logout
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
		return
	}

	// Delete session
	_, err := h.db.Exec(`DELETE FROM sessions WHERE session_id = $1`, claims.SessionID)
	if err != nil {
		h.logger.Error("failed to delete session", zap.Error(err))
	}

	h.audit.LogEvent(r, models.AuditActionLogout, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "",
		map[string]interface{}{"session_id": claims.SessionID})

	h.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgLogoutSuccess)})
}

// Me returns the current user info
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
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
		writeError(w, r, http.StatusNotFound, i18n.MsgUserNotFound)
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// SetupMFA initiates MFA setup
func (h *AuthHandler) SetupMFA(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
		return
	}

	// Generate TOTP secret
	secret, err := h.totpManager.GenerateSecret(claims.Username)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to generate TOTP secret", err)
		return
	}

	// Store secret temporarily (not enabled yet)
	_, err = h.db.Exec(`UPDATE users SET mfa_secret = $1 WHERE id = $2`, secret.Secret, claims.UserID)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to store MFA secret", err)
		return
	}

	writeJSON(w, http.StatusOK, secret)
}

// VerifyMFA verifies and enables MFA
func (h *AuthHandler) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, i18n.MsgBadRequest)
		return
	}

	// Get user's MFA secret
	var secret sql.NullString
	err := h.db.QueryRow(`SELECT mfa_secret FROM users WHERE id = $1`, claims.UserID).Scan(&secret)
	if err != nil || !secret.Valid {
		writeError(w, r, http.StatusBadRequest, i18n.MsgMFANotEnabled)
		return
	}

	// Verify code
	valid, err := h.totpManager.ValidateCode(secret.String, req.Code)
	if err != nil || !valid {
		writeError(w, r, http.StatusBadRequest, i18n.MsgMFAInvalid)
		return
	}

	// Enable MFA
	_, err = h.db.Exec(`UPDATE users SET mfa_enabled = TRUE WHERE id = $1`, claims.UserID)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to enable MFA", err)
		return
	}

	// Generate recovery codes
	recoveryManager := auth.NewRecoveryCodeManager()
	codes, err := recoveryManager.GenerateCodes()
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to generate recovery codes", err)
		return
	}

	// Hash and store recovery codes
	hashedCodes, err := recoveryManager.HashCodes(codes, h.hasher)
	if err != nil {
		h.logger.Error("failed to hash recovery codes", zap.Error(err))
	} else {
		codesJSON, _ := json.Marshal(hashedCodes)
		if _, err := h.db.Exec(`UPDATE users SET recovery_codes = $1 WHERE id = $2`, string(codesJSON), claims.UserID); err != nil {
			h.logger.Error("failed to store recovery codes", zap.Error(err))
		}
	}

	h.audit.LogEvent(r, models.AuditActionMFAEnabled, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":        middleware.T(r.Context(), i18n.MsgMFASetupSuccess),
		"recovery_codes": codes,
	})
}

// DisableMFA disables MFA for the current user
func (h *AuthHandler) DisableMFA(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, i18n.MsgBadRequest)
		return
	}

	// Verify password
	var passwordHash string
	var mfaSecret sql.NullString
	err := h.db.QueryRow(`SELECT password_hash, mfa_secret FROM users WHERE id = $1`, claims.UserID).
		Scan(&passwordHash, &mfaSecret)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query user", err)
		return
	}

	valid, err := h.hasher.Verify(req.Password, passwordHash)
	if err != nil || !valid {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgPasswordMismatch)
		return
	}

	// Verify TOTP code
	if mfaSecret.Valid {
		valid, err := h.totpManager.ValidateCode(mfaSecret.String, req.Code)
		if err != nil || !valid {
			writeError(w, r, http.StatusBadRequest, i18n.MsgMFAInvalid)
			return
		}
	}

	// Disable MFA
	_, err = h.db.Exec(`UPDATE users SET mfa_enabled = FALSE, mfa_secret = NULL, recovery_codes = NULL WHERE id = $1`, claims.UserID)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to disable MFA", err)
		return
	}

	h.audit.LogEvent(r, models.AuditActionMFADisabled, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgMFADisabled)})
}

// ChangePassword handles password change
func (h *AuthHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgUnauthorized)
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, i18n.MsgBadRequest)
		return
	}

	// Validate new password against policy
	if err := h.passwordValidator.Validate(req.NewPassword); err != nil {
		writeError(w, r, http.StatusBadRequest, i18n.MsgPasswordWeak)
		return
	}

	// Get current password hash
	var currentHash string
	err := h.db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, claims.UserID).Scan(&currentHash)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to query user", err)
		return
	}

	// Verify current password
	valid, err := h.hasher.Verify(req.CurrentPassword, currentHash)
	if err != nil || !valid {
		writeError(w, r, http.StatusUnauthorized, i18n.MsgPasswordMismatch)
		return
	}

	// Hash new password
	newHash, err := h.hasher.Hash(req.NewPassword)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to hash password", err)
		return
	}

	// Update password
	_, err = h.db.Exec(`UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`,
		newHash, time.Now(), claims.UserID)
	if err != nil {
		writeInternalError(w, r, h.logger, "failed to update password", err)
		return
	}

	// Invalidate all other sessions
	_, err = h.db.Exec(`DELETE FROM sessions WHERE user_id = $1 AND session_id != $2`,
		claims.UserID, claims.SessionID)
	if err != nil {
		h.logger.Error("failed to invalidate sessions", zap.Error(err))
	}

	h.audit.LogEvent(r, models.AuditActionPasswordChange, models.ResourceTypeUser, strconv.FormatInt(claims.UserID, 10), "", nil)

	writeJSON(w, http.StatusOK, map[string]string{"message": middleware.T(r.Context(), i18n.MsgPasswordChanged)})
}

// verifyAndConsumeRecoveryCode validates a recovery code against the user's
// stored set and atomically marks the matching slot consumed. Returns
// (true, nil) on a valid one-time use; (false, nil) on a wrong code, no
// stored codes, or all slots already consumed; (false, err) on storage
// failure. Concurrent attempts are serialized via SELECT FOR UPDATE so two
// requests cannot consume the same slot.
func (h *AuthHandler) verifyAndConsumeRecoveryCode(userID int64, code string) (bool, error) {
	tx, err := h.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort rollback if commit didn't run

	var stored sql.NullString
	if err := tx.QueryRow(`SELECT recovery_codes FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&stored); err != nil {
		return false, err
	}
	if !stored.Valid || stored.String == "" {
		return false, nil
	}

	var hashed []string
	if err := json.Unmarshal([]byte(stored.String), &hashed); err != nil {
		return false, err
	}

	rm := auth.NewRecoveryCodeManager()
	idx, ok, err := rm.VerifyCode(code, hashed, h.hasher)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	hashed[idx] = "" // consume the slot — VerifyCode skips empty entries
	updated, err := json.Marshal(hashed)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE users SET recovery_codes = $1 WHERE id = $2`, string(updated), userID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
