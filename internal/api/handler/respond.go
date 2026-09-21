package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/api/middleware"
	"github.com/xmpanel/xmpanel/internal/i18n"

	"go.uber.org/zap"
)

// writeJSON is the only place a handler writes a status line; every response
// shares its Content-Type and trailing newline. An encode failure after the
// status line is out has nowhere to go, so it is dropped.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError answers {"error": text} in the request's locale. msg is an i18n
// key, or a literal that i18n.T passes through untouched.
func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": middleware.T(r.Context(), msg)})
}

// writeInternalError logs cause under what, then answers 500 with the generic
// message — the cause never reaches the client.
func writeInternalError(w http.ResponseWriter, r *http.Request, log *zap.Logger, what string, cause error) {
	log.Error(what, zap.Error(cause))
	writeError(w, r, http.StatusInternalServerError, i18n.MsgInternalError)
}

// upstreamStatus maps an adapter failure to the panel's reply. Upstream
// credential problems answer 502, never 401 or 403: the SPA would otherwise
// treat them as its own session expiring and try to refresh the panel JWT.
func upstreamStatus(failure *adapter.Error) (int, string) {
	switch failure.Kind {
	case adapter.NotFound:
		return http.StatusNotFound, resourceMessage(failure.Op, i18n.MsgAccountNotFound, i18n.MsgSessionNotFound, i18n.MsgRoomNotFound, i18n.MsgUpstreamNotFound)
	case adapter.Conflict:
		return http.StatusConflict, resourceMessage(failure.Op, i18n.MsgAccountExists, i18n.MsgUpstreamConflict, i18n.MsgRoomExists, i18n.MsgUpstreamConflict)
	case adapter.Invalid:
		return http.StatusBadRequest, i18n.MsgUpstreamInvalid
	case adapter.NotSupported:
		return http.StatusNotImplemented, i18n.MsgUpstreamNotSupported
	case adapter.RateLimited:
		return http.StatusServiceUnavailable, i18n.MsgUpstreamRateLimited
	case adapter.Unauthorized, adapter.Forbidden:
		return http.StatusBadGateway, i18n.MsgUpstreamCredentials
	case adapter.Unreachable:
		return http.StatusGatewayTimeout, i18n.MsgUpstreamUnreachable
	default:
		return http.StatusBadGateway, i18n.MsgUpstreamFailed
	}
}

// resourceMessage picks the message for the resource class named by the
// operation prefix: accounts.*, sessions.*, rooms.* or anything else.
func resourceMessage(op, account, session, room, other string) string {
	switch {
	case strings.HasPrefix(op, "accounts."):
		return account
	case strings.HasPrefix(op, "sessions."):
		return session
	case strings.HasPrefix(op, "rooms."):
		return room
	}
	return other
}

// writeAdapterError is the only exit for a failed adapter call. A 5xx reply
// is logged at Warn (a failing backend is operational, not a panel defect)
// with operation, status and code; the client sees the classified message.
func writeAdapterError(w http.ResponseWriter, r *http.Request, log *zap.Logger, cause error) {
	failure, ok := adapter.AsError(cause)
	if !ok {
		writeInternalError(w, r, log, "adapter call failed", cause)
		return
	}
	status, message := upstreamStatus(failure)
	if failure.Kind == adapter.RateLimited && failure.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(failure.RetryAfter/time.Second)))
	}
	if status >= http.StatusInternalServerError {
		log.Warn("upstream operation failed",
			zap.String("operation", failure.Op), zap.String("resource", failure.Resource),
			zap.Int("upstream_status", failure.Status), zap.String("upstream_code", failure.Code),
			zap.Error(cause))
	}
	writeError(w, r, status, message)
}

// writeRegistryError answers a failed registry lookup: an unknown server id
// is the caller's 404, a probe failure is the upstream's, anything else is ours.
func writeRegistryError(w http.ResponseWriter, r *http.Request, log *zap.Logger, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	if _, ok := adapter.AsError(err); ok {
		writeAdapterError(w, r, log, err)
		return
	}
	writeInternalError(w, r, log, "failed to get adapter", err)
}
