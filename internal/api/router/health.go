package router

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmpanel/xmpanel/internal/api/handler"
	"github.com/xmpanel/xmpanel/internal/security/crypto"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

const (
	healthDBTimeout   = 2 * time.Second
	healthXMPPTimeout = 2 * time.Second
)

type xmppSummary struct {
	OK     int `json:"ok"`
	Failed int `json:"failed"`
}

type healthResponse struct {
	Status   string       `json:"status"` // "ok" | "degraded" | "error"
	Database bool         `json:"database"`
	XMPP     *xmppSummary `json:"xmpp,omitempty"`
}

// newHealthHandler returns the public liveness probe handler. It pings the
// PostgreSQL connection and every enabled XMPP server to summarize
// reachability for monitoring tools.
//
// Response shape is intentionally minimal: only aggregate ok/failed counts
// for XMPP, never per-server names, IDs, IPs, or latencies. /health is a
// public endpoint and detailed disclosure would help fingerprint the
// deployment.
//
// HTTP status:
//   - 200 + status=ok        database reachable, all XMPP servers responded.
//   - 200 + status=degraded  database reachable but some XMPP servers failed.
//                            The panel itself is healthy, so systemd liveness
//                            should not restart on this.
//   - 503 + status=error     database unreachable; panel is broken.
//
// Each probe has a 2s timeout. XMPP probes run concurrently so worst-case
// total latency is ~2s regardless of server count.
func newHealthHandler(db *store.DB, keyRing *crypto.KeyRing, logger *zap.Logger) http.HandlerFunc {
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

		// 2. List enabled XMPP server IDs. A query failure here doesn't fail
		// the whole probe — DB ping already showed connectivity, this is just
		// missing a non-critical breakdown.
		ids, err := listEnabledXMPPServerIDs(db)
		if err != nil {
			logger.Warn("health check: failed to list XMPP servers", zap.Error(err))
			_ = json.NewEncoder(w).Encode(healthResponse{
				Status:   "ok",
				Database: true,
			})
			return
		}

		if len(ids) == 0 {
			// No XMPP servers configured — valid state, omit the xmpp block.
			_ = json.NewEncoder(w).Encode(healthResponse{
				Status:   "ok",
				Database: true,
			})
			return
		}

		summary := pingXMPPServers(r.Context(), db, keyRing, ids)
		status := "ok"
		if summary.Failed > 0 {
			status = "degraded"
		}
		_ = json.NewEncoder(w).Encode(healthResponse{
			Status:   status,
			Database: true,
			XMPP:     &summary,
		})
	}
}

// listEnabledXMPPServerIDs returns just the IDs of servers with enabled=true.
// Adapter construction reloads the full row inside GetXMPPAdapter, so we
// don't need name/host/port here.
func listEnabledXMPPServerIDs(db *store.DB) ([]int64, error) {
	rows, err := db.Query(`SELECT id FROM xmpp_servers WHERE enabled = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// pingXMPPServers probes each server concurrently with a per-probe timeout.
// Returns aggregate counts only — the caller surfaces ok/failed in the public
// response, never per-server detail.
func pingXMPPServers(ctx context.Context, db *store.DB, keyRing *crypto.KeyRing, ids []int64) xmppSummary {
	var ok, failed int64
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(serverID int64) {
			defer wg.Done()
			a, err := handler.GetXMPPAdapter(db, keyRing, serverID)
			if err != nil {
				atomic.AddInt64(&failed, 1)
				return
			}
			pingCtx, cancel := context.WithTimeout(ctx, healthXMPPTimeout)
			defer cancel()
			if err := a.Ping(pingCtx); err != nil {
				atomic.AddInt64(&failed, 1)
				return
			}
			atomic.AddInt64(&ok, 1)
		}(id)
	}
	wg.Wait()
	return xmppSummary{OK: int(ok), Failed: int(failed)}
}
