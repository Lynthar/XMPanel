package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

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

var adapterResponses = map[string]struct {
	failed, notFound, conflict string
}{
	"server.stats":           {failed: "Failed to get server statistics"},
	"accounts.list":          {failed: "Failed to list users"},
	"accounts.get":           {failed: "Failed to get user", notFound: i18n.MsgUserNotFound},
	"accounts.create":        {failed: "Failed to create user", conflict: "User already exists"},
	"accounts.delete":        {failed: "Failed to delete user", notFound: i18n.MsgUserNotFound},
	"sessions.list":          {failed: "Failed to list sessions"},
	"sessions.terminate":     {failed: "Failed to kick session"},
	"sessions.terminate_all": {failed: "Failed to kick user"},
	"rooms.list":             {failed: "Failed to list rooms"},
	"rooms.get":              {failed: "Failed to get room", notFound: "Room not found"},
	"rooms.create":           {failed: "Failed to create room", conflict: "Room already exists"},
	"rooms.delete":           {failed: "Failed to delete room", notFound: "Room not found"},
}

// XMPP routes expose resource failures only where their response contract
// defines them; all other upstream failures remain 502, including credentials
// and unsupported operations, so clients never refresh the panel's JWT for them.
func writeAdapterError(w http.ResponseWriter, r *http.Request, log *zap.Logger, op string, cause error) {
	response := adapterResponses[op]
	status, message := http.StatusBadGateway, response.failed
	if message == "" {
		message = i18n.MsgInternalError
	}
	fields := []zap.Field{zap.String("operation", op), zap.Error(cause)}
	var failure *adapter.Error
	if errors.As(cause, &failure) {
		fields = append(fields, zap.String("upstream_operation", failure.Op),
			zap.String("resource", failure.Resource), zap.Int("upstream_status", failure.Status),
			zap.String("upstream_code", failure.Code))
		switch failure.Kind {
		case adapter.NotFound:
			if response.notFound != "" {
				status, message = http.StatusNotFound, response.notFound
			}
		case adapter.Conflict:
			if response.conflict != "" {
				status, message = http.StatusConflict, response.conflict
			}
		}
	}
	if status == http.StatusBadGateway {
		log.Error(message, fields...)
	}
	writeError(w, r, status, message)
}

// writeServerLookupError answers a failed registry lookup: an unknown server id
// is the caller's 404, anything else is ours.
func writeServerLookupError(w http.ResponseWriter, r *http.Request, log *zap.Logger, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, i18n.MsgServerNotFound)
		return
	}
	writeInternalError(w, r, log, "failed to get adapter", err)
}
