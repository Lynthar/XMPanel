package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xmpanel/xmpanel/internal/monitor"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

// The public probe answers from the monitor's snapshot. It reports aggregate
// counts only, and a request never reaches a backend: the handler is not given
// the registry at all.
func TestHealthReadsTheMonitorSnapshot(t *testing.T) {
	db := storetest.NewDB(t)
	var round *monitor.Health
	handler := newHealthHandler(db, func() *monitor.Health { return round }, zap.NewNop())

	// Before the first round there is nothing to report about the backends,
	// and a freshly started panel must not claim they are down.
	body := probeHealth(t, handler, http.StatusOK)
	if body.Status != "ok" || !body.Database || body.Backends != nil {
		t.Fatalf("before the first round: %+v", body)
	}

	round = &monitor.Health{OK: 2}
	body = probeHealth(t, handler, http.StatusOK)
	if body.Status != "ok" || body.Backends == nil || body.Backends.OK != 2 || body.Backends.Failed != 0 {
		t.Fatalf("all reachable: %+v", body)
	}

	round = &monitor.Health{OK: 1, Failed: 1}
	body = probeHealth(t, handler, http.StatusOK)
	if body.Status != "degraded" || body.Backends.Failed != 1 {
		t.Fatalf("one unreachable: %+v", body)
	}

	// No registered server is a valid state, not a degraded one.
	round = &monitor.Health{}
	body = probeHealth(t, handler, http.StatusOK)
	if body.Status != "ok" || body.Backends != nil {
		t.Fatalf("no servers: %+v", body)
	}
}

// An unreachable database is the one condition that makes the panel itself
// unhealthy, so it answers 503 and says nothing about the backends.
func TestHealthFailsOnDatabaseLoss(t *testing.T) {
	db := storetest.NewDB(t)
	handler := newHealthHandler(db, func() *monitor.Health { return &monitor.Health{OK: 3} }, zap.NewNop())
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	body := probeHealth(t, handler, http.StatusServiceUnavailable)
	if body.Status != "error" || body.Database || body.Backends != nil {
		t.Fatalf("after losing the database: %+v", body)
	}
}

func probeHealth(t *testing.T, handler http.HandlerFunc, wantStatus int) healthResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
	}
	var body healthResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}
