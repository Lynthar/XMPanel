package i18n

import (
	"net/http"
	"strings"
)

// Locale represents a supported language
type Locale string

const (
	LocaleEN Locale = "en"
	LocaleZH Locale = "zh"
)

// DefaultLocale is the fallback locale when detection fails
const DefaultLocale = LocaleEN

// SupportedLocales lists all supported locales
var SupportedLocales = []Locale{LocaleEN, LocaleZH}

// Message keys for API responses
const (
	// Auth messages
	MsgLoginSuccess        = "auth.login.success"
	MsgLoginFailed         = "auth.login.failed"
	MsgInvalidCredentials  = "auth.invalid_credentials"
	MsgAccountLocked       = "auth.account_locked"
	MsgMFARequired         = "auth.mfa_required"
	MsgMFAInvalid          = "auth.mfa_invalid"
	MsgTokenExpired        = "auth.token_expired"
	MsgTokenInvalid        = "auth.token_invalid"
	MsgSessionRevoked      = "auth.session_revoked"
	MsgLogoutSuccess       = "auth.logout.success"
	MsgPasswordChanged     = "auth.password_changed"
	MsgPasswordWeak        = "auth.password_weak"
	MsgPasswordMismatch    = "auth.password_mismatch"
	MsgMFASetupSuccess     = "auth.mfa_setup.success"
	MsgMFADisabled         = "auth.mfa_disabled"
	MsgMFAAlreadyEnabled   = "auth.mfa_already_enabled"
	MsgMFANotEnabled       = "auth.mfa_not_enabled"
	MsgRecoveryCodeInvalid = "auth.recovery_code_invalid"

	// User messages
	MsgUserCreated       = "user.created"
	MsgUserUpdated       = "user.updated"
	MsgUserDeleted       = "user.deleted"
	MsgUserNotFound      = "user.not_found"
	MsgUserAlreadyExists = "user.already_exists"
	MsgCannotDeleteSelf  = "user.cannot_delete_self"

	// Server messages
	MsgServerCreated     = "server.created"
	MsgServerUpdated     = "server.updated"
	MsgServerDeleted     = "server.deleted"
	MsgServerNotFound    = "server.not_found"
	MsgServerConnFailed  = "server.connection_failed"
	MsgServerTestSuccess = "server.test_success"
	MsgServerTestFailed  = "server.test_failed"

	// Backend (managed server) messages
	MsgAccountCreated        = "backend.account.created"
	MsgAccountDeleted        = "backend.account.deleted"
	MsgAccountUpdated        = "backend.account.updated"
	MsgAccountNotFound       = "backend.account.not_found"
	MsgAccountExists         = "backend.account.exists"
	MsgRoomCreated           = "backend.room.created"
	MsgRoomDeleted           = "backend.room.deleted"
	MsgRoomNotFound          = "backend.room.not_found"
	MsgRoomExists            = "backend.room.exists"
	MsgSessionTerminated     = "backend.session.terminated"
	MsgSessionsTerminated    = "backend.sessions.terminated"
	MsgSessionNotFound       = "backend.session.not_found"
	MsgUpstreamNotFound      = "backend.upstream.not_found"
	MsgUpstreamConflict      = "backend.upstream.conflict"
	MsgUpstreamInvalid       = "backend.upstream.invalid"
	MsgUpstreamRateLimited   = "backend.upstream.rate_limited"
	MsgUpstreamNotSupported  = "backend.upstream.not_supported"
	MsgUpstreamCredentials   = "backend.upstream.credentials"
	MsgUpstreamUnreachable   = "backend.upstream.unreachable"
	MsgUpstreamFailed        = "backend.upstream.failed"
	MsgUnsupportedServerType = "server.unsupported_type"

	// Common messages
	MsgInternalError     = "error.internal"
	MsgBadRequest        = "error.bad_request"
	MsgUnauthorized      = "error.unauthorized"
	MsgForbidden         = "error.forbidden"
	MsgNotFound          = "error.not_found"
	MsgValidationFailed  = "error.validation_failed"
	MsgRateLimitExceeded = "error.rate_limit"
	MsgInvalidInput      = "error.invalid_input"

	// Validation messages
	MsgFieldRequired = "validation.field_required"
	MsgFieldTooShort = "validation.field_too_short"
	MsgFieldTooLong  = "validation.field_too_long"
	MsgInvalidEmail  = "validation.invalid_email"
	MsgInvalidFormat = "validation.invalid_format"

	// Password validation
	MsgPasswordTooShort  = "password.too_short"
	MsgPasswordNoUpper   = "password.no_uppercase"
	MsgPasswordNoLower   = "password.no_lowercase"
	MsgPasswordNoNumber  = "password.no_number"
	MsgPasswordNoSpecial = "password.no_special"
)

// messages contains all translations
var messages = map[Locale]map[string]string{
	LocaleEN: {
		// Auth
		MsgLoginSuccess:        "Login successful",
		MsgLoginFailed:         "Login failed",
		MsgInvalidCredentials:  "Invalid username or password",
		MsgAccountLocked:       "Account is locked due to too many failed attempts",
		MsgMFARequired:         "Two-factor authentication required",
		MsgMFAInvalid:          "Invalid verification code",
		MsgTokenExpired:        "Session has expired, please login again",
		MsgTokenInvalid:        "Invalid authentication token",
		MsgSessionRevoked:      "Session has been revoked",
		MsgLogoutSuccess:       "Logged out successfully",
		MsgPasswordChanged:     "Password changed successfully",
		MsgPasswordWeak:        "Password does not meet security requirements",
		MsgPasswordMismatch:    "Current password is incorrect",
		MsgMFASetupSuccess:     "Two-factor authentication enabled",
		MsgMFADisabled:         "Two-factor authentication disabled",
		MsgMFAAlreadyEnabled:   "Two-factor authentication is already enabled",
		MsgMFANotEnabled:       "Two-factor authentication is not enabled",
		MsgRecoveryCodeInvalid: "Invalid recovery code",

		// User
		MsgUserCreated:       "User created successfully",
		MsgUserUpdated:       "User updated successfully",
		MsgUserDeleted:       "User deleted successfully",
		MsgUserNotFound:      "User not found",
		MsgUserAlreadyExists: "Username or email already exists",
		MsgCannotDeleteSelf:  "Cannot delete your own account",

		// Server
		MsgServerCreated:     "Server added successfully",
		MsgServerUpdated:     "Server updated successfully",
		MsgServerDeleted:     "Server deleted successfully",
		MsgServerNotFound:    "Server not found",
		MsgServerConnFailed:  "Failed to connect to server",
		MsgServerTestSuccess: "Connection test successful",
		MsgServerTestFailed:  "Connection test failed",

		// Backend
		MsgAccountCreated:        "Account created",
		MsgAccountDeleted:        "Account deleted",
		MsgAccountUpdated:        "Account updated",
		MsgAccountNotFound:       "Account not found",
		MsgAccountExists:         "Account already exists",
		MsgRoomCreated:           "Room created",
		MsgRoomDeleted:           "Room deleted",
		MsgRoomNotFound:          "Room not found",
		MsgRoomExists:            "Room already exists",
		MsgSessionTerminated:     "Session terminated",
		MsgSessionsTerminated:    "All sessions terminated",
		MsgSessionNotFound:       "Session not found",
		MsgUpstreamNotFound:      "Not found on the server",
		MsgUpstreamConflict:      "Already exists on the server",
		MsgUpstreamInvalid:       "The server rejected the request",
		MsgUpstreamRateLimited:   "The server is rate limiting requests",
		MsgUpstreamNotSupported:  "This server does not support the operation",
		MsgUpstreamCredentials:   "The server rejected the stored credentials",
		MsgUpstreamUnreachable:   "The server could not be reached",
		MsgUpstreamFailed:        "The server failed to complete the operation",
		MsgUnsupportedServerType: "Unsupported server protocol or implementation",

		// Common errors
		MsgInternalError:     "Internal server error",
		MsgBadRequest:        "Bad request",
		MsgUnauthorized:      "Authentication required",
		MsgForbidden:         "Access denied",
		MsgNotFound:          "Resource not found",
		MsgValidationFailed:  "Validation failed",
		MsgRateLimitExceeded: "Too many requests, please try again later",
		MsgInvalidInput:      "Invalid input data",

		// Validation
		MsgFieldRequired: "This field is required",
		MsgFieldTooShort: "Input is too short",
		MsgFieldTooLong:  "Input is too long",
		MsgInvalidEmail:  "Invalid email address",
		MsgInvalidFormat: "Invalid format",

		// Password
		MsgPasswordTooShort:  "Password must be at least %d characters",
		MsgPasswordNoUpper:   "Password must contain at least one uppercase letter",
		MsgPasswordNoLower:   "Password must contain at least one lowercase letter",
		MsgPasswordNoNumber:  "Password must contain at least one number",
		MsgPasswordNoSpecial: "Password must contain at least one special character",
	},
	LocaleZH: {
		// Auth
		MsgLoginSuccess:        "登录成功",
		MsgLoginFailed:         "登录失败",
		MsgInvalidCredentials:  "用户名或密码错误",
		MsgAccountLocked:       "账户已被锁定，登录尝试次数过多",
		MsgMFARequired:         "需要双因素认证",
		MsgMFAInvalid:          "验证码无效",
		MsgTokenExpired:        "会话已过期，请重新登录",
		MsgTokenInvalid:        "认证令牌无效",
		MsgSessionRevoked:      "会话已被撤销",
		MsgLogoutSuccess:       "已成功登出",
		MsgPasswordChanged:     "密码修改成功",
		MsgPasswordWeak:        "密码不符合安全要求",
		MsgPasswordMismatch:    "当前密码不正确",
		MsgMFASetupSuccess:     "双因素认证已启用",
		MsgMFADisabled:         "双因素认证已禁用",
		MsgMFAAlreadyEnabled:   "双因素认证已经启用",
		MsgMFANotEnabled:       "双因素认证尚未启用",
		MsgRecoveryCodeInvalid: "恢复码无效",

		// User
		MsgUserCreated:       "用户创建成功",
		MsgUserUpdated:       "用户更新成功",
		MsgUserDeleted:       "用户删除成功",
		MsgUserNotFound:      "用户不存在",
		MsgUserAlreadyExists: "用户名或邮箱已存在",
		MsgCannotDeleteSelf:  "不能删除自己的账户",

		// Server
		MsgServerCreated:     "服务器添加成功",
		MsgServerUpdated:     "服务器更新成功",
		MsgServerDeleted:     "服务器删除成功",
		MsgServerNotFound:    "服务器不存在",
		MsgServerConnFailed:  "连接服务器失败",
		MsgServerTestSuccess: "连接测试成功",
		MsgServerTestFailed:  "连接测试失败",

		// XMPP
		MsgAccountCreated:        "账号已创建",
		MsgAccountDeleted:        "账号已删除",
		MsgAccountUpdated:        "账号已更新",
		MsgAccountNotFound:       "账号不存在",
		MsgAccountExists:         "账号已存在",
		MsgRoomCreated:           "房间已创建",
		MsgRoomDeleted:           "房间已删除",
		MsgRoomNotFound:          "房间不存在",
		MsgRoomExists:            "房间已存在",
		MsgSessionTerminated:     "会话已终止",
		MsgSessionsTerminated:    "全部会话已终止",
		MsgSessionNotFound:       "会话不存在",
		MsgUpstreamNotFound:      "服务器上不存在该对象",
		MsgUpstreamConflict:      "服务器上已存在该对象",
		MsgUpstreamInvalid:       "服务器拒绝了该请求",
		MsgUpstreamRateLimited:   "服务器正在限流",
		MsgUpstreamNotSupported:  "该服务器不支持此操作",
		MsgUpstreamCredentials:   "服务器拒绝了保存的凭据",
		MsgUpstreamUnreachable:   "无法连接到服务器",
		MsgUpstreamFailed:        "服务器未能完成该操作",
		MsgUnsupportedServerType: "不支持的服务器协议或实现",

		// Common errors
		MsgInternalError:     "服务器内部错误",
		MsgBadRequest:        "请求格式错误",
		MsgUnauthorized:      "请先登录",
		MsgForbidden:         "没有访问权限",
		MsgNotFound:          "资源不存在",
		MsgValidationFailed:  "数据验证失败",
		MsgRateLimitExceeded: "请求过于频繁，请稍后再试",
		MsgInvalidInput:      "输入数据无效",

		// Validation
		MsgFieldRequired: "此字段为必填项",
		MsgFieldTooShort: "输入内容过短",
		MsgFieldTooLong:  "输入内容过长",
		MsgInvalidEmail:  "邮箱地址格式无效",
		MsgInvalidFormat: "格式无效",

		// Password
		MsgPasswordTooShort:  "密码长度至少为 %d 个字符",
		MsgPasswordNoUpper:   "密码必须包含至少一个大写字母",
		MsgPasswordNoLower:   "密码必须包含至少一个小写字母",
		MsgPasswordNoNumber:  "密码必须包含至少一个数字",
		MsgPasswordNoSpecial: "密码必须包含至少一个特殊字符",
	},
}

// T returns the translated message for the given locale and key
func T(locale Locale, key string) string {
	if msgs, ok := messages[locale]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	// Fallback to English
	if msgs, ok := messages[LocaleEN]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	// Return key if no translation found
	return key
}

// TWithFallback returns the translated message, or the fallback if key not found
func TWithFallback(locale Locale, key, fallback string) string {
	if msgs, ok := messages[locale]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	if msgs, ok := messages[LocaleEN]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	return fallback
}

// DetectLocale detects the locale from the Accept-Language header
func DetectLocale(r *http.Request) Locale {
	acceptLang := r.Header.Get("Accept-Language")
	if acceptLang == "" {
		return DefaultLocale
	}

	// Parse Accept-Language header (simplified parsing)
	// Format: en-US,en;q=0.9,zh-CN;q=0.8,zh;q=0.7
	for _, part := range strings.Split(acceptLang, ",") {
		lang := strings.TrimSpace(strings.Split(part, ";")[0])
		lang = strings.ToLower(lang)

		// Check for exact match first
		for _, supported := range SupportedLocales {
			if lang == string(supported) {
				return supported
			}
		}

		// Check for prefix match (e.g., "zh-CN" -> "zh")
		langPrefix := strings.Split(lang, "-")[0]
		for _, supported := range SupportedLocales {
			if langPrefix == string(supported) {
				return supported
			}
		}
	}

	return DefaultLocale
}

// IsSupported checks if a locale is supported
func IsSupported(locale string) bool {
	for _, l := range SupportedLocales {
		if locale == string(l) {
			return true
		}
	}
	return false
}

// GetLocale returns the Locale type from string, defaulting to English if invalid
func GetLocale(locale string) Locale {
	if IsSupported(locale) {
		return Locale(locale)
	}
	return DefaultLocale
}
