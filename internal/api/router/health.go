package router

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/xmpanel/xmpanel/internal/monitor"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

const healthDBTimeout = 2 * time.Second

// backendHealth reports the monitor's last completed sample round, or nil
// before the first one finishes. Taking the reading as a function is what
// keeps the registry out of the public request path entirely.
type backendHealth func() *monitor.Health

type backendSummary struct {
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

type healthResponse struct {
	Status   string          `json:"status"` // "ok" | "degraded" | "error"
	Database bool            `json:"database"`
	Backends *backendSummary `json:"backends,omitempty"`
}

// newHealthHandler returns the public liveness probe handler. It pings the
// PostgreSQL connection and reports the backend counts from the monitor's
// latest sample round, which is at most one sample interval old.
//
// The request never probes a backend itself: /health is public, so a request
// path that reaches every registered server would let anyone outside make the
// panel generate traffic to all of them.
//
// Response shape is intentionally minimal: only aggregate ok/failed counts
// for backends, never per-server names, IDs, IPs, latencies, or the age of the
// sample. /health is a public endpoint and detailed disclosure would help
// fingerprint the deployment.
//
// HTTP status:
//   - 200 + status=ok        database reachable, all backends responded.
//   - 200 + status=degraded  database reachable but some backends failed.
//     The panel itself is healthy, so systemd liveness
//     should not restart on this.
//   - 503 + status=error     database unreachable; panel is broken.
//
// The backends block is omitted when no server is registered, and also before
// the monitor's first round finishes — a panel that has just started reports
// on itself rather than guessing about its backends.
func newHealthHandler(db *store.DB, backends backendHealth, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		// Database ping — short-circuit on failure since the panel can't
		// function without the DB.
		dbCtx, cancel := context.WithTimeout(r.Context(), healthDBTimeout)
		defer cancel()
		if err := db.PingContext(dbCtx); err != nil {
			logger.Warn("health check: database ping failed", zap.Error(err))
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(healthResponse{
				Status:   "error",
				Database: false,
			})
			return
		}

		response := healthResponse{Status: "ok", Database: true}
		if health := backends(); health != nil && health.OK+health.Failed > 0 {
			response.Backends = &backendSummary{OK: health.OK, Failed: health.Failed}
			if health.Failed > 0 {
				response.Status = "degraded"
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}
}
