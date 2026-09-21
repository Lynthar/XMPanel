package router

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

const (
	healthDBTimeout      = 2 * time.Second
	healthBackendTimeout = 2 * time.Second
)

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
// PostgreSQL connection and probes every enabled backend to summarize
// reachability for monitoring tools.
//
// Response shape is intentionally minimal: only aggregate ok/failed counts
// for backends, never per-server names, IDs, IPs, or latencies. /health is a
// public endpoint and detailed disclosure would help fingerprint the
// deployment.
//
// HTTP status:
//   - 200 + status=ok        database reachable, all backends responded.
//   - 200 + status=degraded  database reachable but some backends failed.
//     The panel itself is healthy, so systemd liveness
//     should not restart on this.
//   - 503 + status=error     database unreachable; panel is broken.
//
// Each probe has a 2s timeout. Backend probes run concurrently so worst-case
// total latency is ~2s regardless of server count.
func newHealthHandler(db *store.DB, adapters *registry.Registry, logger *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		// 1. Database ping — short-circuit on failure since the panel can't
		// function without the DB and probing XMPP would just waste 2s.
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

		// 2. List enabled server IDs. A query failure here doesn't fail
		// the whole probe — DB ping already showed connectivity, this is just
		// missing a non-critical breakdown.
		ids, err := listEnabledServerIDs(db)
		if err != nil {
			logger.Warn("health check: failed to list servers", zap.Error(err))
			_ = json.NewEncoder(w).Encode(healthResponse{
				Status:   "ok",
				Database: true,
			})
			return
		}

		if len(ids) == 0 {
			// No servers configured — valid state, omit the backends block.
			_ = json.NewEncoder(w).Encode(healthResponse{
				Status:   "ok",
				Database: true,
			})
			return
		}

		summary := probeServers(r.Context(), adapters, ids)
		status := "ok"
		if summary.Failed > 0 {
			status = "degraded"
		}
		_ = json.NewEncoder(w).Encode(healthResponse{
			Status:   status,
			Database: true,
			Backends: &summary,
		})
	}
}

// listEnabledServerIDs returns just the IDs of servers with enabled=true.
// The registry loads the full row only when constructing a client.
func listEnabledServerIDs(db *store.DB) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM servers WHERE enabled = TRUE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// probeServers probes each server concurrently with a per-probe timeout.
// Returns aggregate counts only — the caller surfaces ok/failed in the public
// response, never per-server detail. A server whose last probe failed is
// answered from the registry's cache until its retry window passes.
func probeServers(ctx context.Context, adapters *registry.Registry, ids []int64) backendSummary {
	var ok, failed int64
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(serverID int64) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, healthBackendTimeout)
			defer cancel()
			a, _, err := adapters.Get(probeCtx, serverID)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				return
			}
			if _, err := a.Probe(probeCtx); err != nil {
				atomic.AddInt64(&failed, 1)
				return
			}
			atomic.AddInt64(&ok, 1)
		}(id)
	}
	wg.Wait()
	return backendSummary{OK: int(ok), Failed: int(failed)}
}
