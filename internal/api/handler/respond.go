package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

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

// writeUpstreamError answers 502 when the XMPP server failed the operation;
// msg is both the log line and the response body.
func writeUpstreamError(w http.ResponseWriter, r *http.Request, log *zap.Logger, msg string, cause error) {
	log.Error(msg, zap.Error(cause))
	writeError(w, r, http.StatusBadGateway, msg)
}

// writeServerLookupError answers a failed GetXMPPAdapter: an unknown server id
// is the caller's 404, anything else is ours.
func writeServerLookupError(w http.ResponseWriter, r *http.Request, log *zap.Logger, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	writeInternalError(w, r, log, "failed to get adapter", err)
}
