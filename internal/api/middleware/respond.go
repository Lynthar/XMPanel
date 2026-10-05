package middleware

import (
	"encoding/json"
	"net/http"
)

// writeError answers {"error": text} in the request's locale, the shape every
// handler uses; the SPA reads that field and shows only a generic line without it.
func writeError(w http.ResponseWriter, r *http.Request, status int, key string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": T(r.Context(), key)})
}
