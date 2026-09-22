package monitor

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/prosody/prosodytest"
	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/store"
	"github.com/xmpanel/xmpanel/internal/store/storetest"

	"go.uber.org/zap"
)

// newMonitor wires a monitor against one fake Prosody, and returns the server
// row id so tests can address it.
func newMonitor(t *testing.T) (*Monitor, *store.DB, int64, *prosodytest.Fake) {
	t.Helper()
	db := storetest.NewDB(t)
	ring := storetest.NewKeyRing(t)
	fake := prosodytest.NewFake()
	fake.Reset()
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)

	id := storetest.InsertServer(t, db, ring, "prosody", upstream.URL, prosodytest.Domain, prosodytest.Token)
	adapters := registry.New(db, ring, zap.NewNop())
	t.Cleanup(adapters.Close)
	return New(db, adapters, config.DefaultConfig().Monitor, zap.NewNop()), db, id, fake
}

// A sample round records one row per enabled server and only then publishes
// the counts /health reports.
func TestSampleRoundRecordsAndPublishes(t *testing.T) {
	m, db, id, _ := newMonitor(t)
	if health := m.Health(); health != nil {
		t.Fatalf("health before the first round = %+v, want nil", health)
	}

	m.sampleRound(context.Background())

	health := m.Health()
	if health == nil || health.OK != 1 || health.Failed != 0 {
		t.Fatalf("health = %+v, want 1 ok", health)
	}
	var ok bool
	var latency *int
	err := db.QueryRow(`SELECT ok, latency_ms FROM server_samples WHERE server_id = $1`, id).Scan(&ok, &latency)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	if !ok || latency == nil {
		t.Fatalf("sample ok=%v latency=%v, want a reachable sample with a latency", ok, latency)
	}
}

// An unreachable backend is recorded as a failed sample, not as a missing row:
// the gap in the counters is what the Overview curve has to show.
func TestSampleRoundRecordsFailure(t *testing.T) {
	m, db, id, fake := newMonitor(t)
	fake.FailWith(500)

	m.sampleRound(context.Background())

	var ok bool
	var latency, online *int
	err := db.QueryRow(`SELECT ok, latency_ms, online_users FROM server_samples WHERE server_id = $1`, id).Scan(&ok, &latency, &online)
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	if ok || latency != nil || online != nil {
		t.Fatalf("sample ok=%v latency=%v online=%v, want a failed sample with no readings", ok, latency, online)
	}
	if health := m.Health(); health == nil || health.Failed != 1 {
		t.Fatalf("health = %+v, want 1 failed", health)
	}
}

// A disabled server is not sampled at all; neither its rows nor its result
// reach the health snapshot.
func TestSampleRoundSkipsDisabledServers(t *testing.T) {
	m, db, id, _ := newMonitor(t)
	if _, err := db.Exec(`UPDATE servers SET enabled = FALSE WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	m.sampleRound(context.Background())

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM server_samples WHERE server_id = $1`, id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("stored %d samples for a disabled server", rows)
	}
	if health := m.Health(); health == nil || health.OK+health.Failed != 0 {
		t.Fatalf("health = %+v, want no backends", health)
	}
}

// Samples are bucketed so a long window stays small, the counters follow the
// last reading in each bucket, and a bucket with no reachable sample leaves a
// gap rather than a zero.
func TestQuerySamplesBuckets(t *testing.T) {
	_, db, id, _ := newMonitor(t)
	// Buckets are aligned to a fixed epoch, so a base on a ten-minute mark
	// puts both of the first two ticks in the same bucket.
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(10 * time.Minute)
	insert := func(offset time.Duration, ok bool, latency, online int) {
		t.Helper()
		var latencyValue, onlineValue any
		if ok {
			latencyValue, onlineValue = latency, online
		}
		_, err := db.Exec(`INSERT INTO server_samples (server_id, ts, ok, latency_ms, online_users)
			VALUES ($1, $2, $3, $4, $5)`, id, base.Add(offset), ok, latencyValue, onlineValue)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Two ticks in the first bucket, one of them failed; the second bucket has
	// no reachable sample at all.
	insert(0, true, 10, 5)
	insert(time.Minute, false, 0, 0)
	insert(10*time.Minute, false, 0, 0)

	series, err := QuerySamples(context.Background(), db, id, time.Hour, 10*time.Minute)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if series.BucketSeconds != 600 {
		t.Fatalf("bucket = %ds, want the sample interval", series.BucketSeconds)
	}
	if len(series.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(series.Points))
	}
	first, second := series.Points[0], series.Points[1]
	if first.OKRatio != 0.5 {
		t.Errorf("first ok_ratio = %v, want 0.5", first.OKRatio)
	}
	if first.LatencyMS == nil || *first.LatencyMS != 10 {
		t.Errorf("first latency = %v, want the reachable sample's", first.LatencyMS)
	}
	if first.OnlineUsers == nil || *first.OnlineUsers != 5 {
		t.Errorf("first online = %v, want 5", first.OnlineUsers)
	}
	if second.OKRatio != 0 || second.LatencyMS != nil || second.OnlineUsers != nil {
		t.Errorf("second bucket = %+v, want an empty gap", second)
	}
}

// A window wider than maxPoints buckets is aggregated rather than returned
// tick by tick, so thirty days do not reach the browser as 43,200 points.
func TestQuerySamplesCapsPointCount(t *testing.T) {
	_, db, id, _ := newMonitor(t)
	series, err := QuerySamples(context.Background(), db, id, 30*24*time.Hour, time.Minute)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if want := int((30 * 24 * time.Hour / maxPoints).Seconds()); series.BucketSeconds != want {
		t.Fatalf("bucket = %ds, want %ds", series.BucketSeconds, want)
	}
}

// An unknown range is not silently widened, and a server with no history
// answers with an empty window rather than an error.
func TestQuerySamplesEmpty(t *testing.T) {
	_, db, id, _ := newMonitor(t)
	series, err := QuerySamples(context.Background(), db, id, time.Hour, time.Minute)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(series.Points) != 0 {
		t.Fatalf("points = %d, want none", len(series.Points))
	}
	if !series.To.After(series.From) {
		t.Fatalf("window %v..%v is not forward", series.From, series.To)
	}
}

// Only the latest result per kind is kept, so a check that recovers leaves no
// stale failure behind.
func TestStoreCheckReplacesPreviousResult(t *testing.T) {
	_, db, id, _ := newMonitor(t)
	ctx := context.Background()
	failed := checkResult(KindDNSSRV, CheckItem{Target: "_xmpp-client._tcp." + prosodytest.Domain, Status: StatusFail, Error: "nope"})
	if err := storeCheck(ctx, db, id, failed); err != nil {
		t.Fatal(err)
	}
	recovered := checkResult(KindDNSSRV, CheckItem{Target: "_xmpp-client._tcp." + prosodytest.Domain, Status: StatusOK, Values: []string{"xmpp.example.com:5222"}})
	if err := storeCheck(ctx, db, id, recovered); err != nil {
		t.Fatal(err)
	}

	checks, err := QueryChecks(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1", len(checks))
	}
	if checks[0].Status != StatusOK || len(checks[0].Detail.Items) != 1 {
		t.Fatalf("check = %+v, want the recovered result", checks[0])
	}
	if got := checks[0].Detail.Items[0].Values; len(got) != 1 || got[0] != "xmpp.example.com:5222" {
		t.Fatalf("values = %v, want the resolved target", got)
	}
}

// Retention deletes samples past the window and the check rows of a kind that
// stopped being recorded, and leaves everything inside it alone.
func TestDeleteExpired(t *testing.T) {
	_, db, id, _ := newMonitor(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, age := range []time.Duration{2 * time.Hour, 30 * time.Minute} {
		if _, err := db.Exec(`INSERT INTO server_samples (server_id, ts, ok) VALUES ($1, $2, TRUE)`, id, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	stale := checkResult(KindWellKnown)
	stale.TS = now.Add(-2 * time.Hour)
	if err := storeCheck(ctx, db, id, stale); err != nil {
		t.Fatal(err)
	}
	if err := storeCheck(ctx, db, id, checkResult(KindDNSSRV)); err != nil {
		t.Fatal(err)
	}

	if err := deleteExpired(ctx, db, time.Hour); err != nil {
		t.Fatal(err)
	}

	var samples int
	if err := db.QueryRow(`SELECT count(*) FROM server_samples WHERE server_id = $1`, id).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 1 {
		t.Fatalf("samples = %d, want only the recent one", samples)
	}
	checks, err := QueryChecks(ctx, db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Kind != KindDNSSRV {
		t.Fatalf("checks = %+v, want only the refreshed kind", checks)
	}
}

// Deleting a server takes its monitoring history with it; nothing is left
// pointing at a row that no longer exists.
func TestDeletingServerCascades(t *testing.T) {
	m, db, id, _ := newMonitor(t)
	m.sampleRound(context.Background())
	if err := storeCheck(context.Background(), db, id, checkResult(KindDNSSRV)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM servers WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	var samples, checks int
	if err := db.QueryRow(`SELECT count(*) FROM server_samples WHERE server_id = $1`, id).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM server_checks WHERE server_id = $1`, id).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if samples != 0 || checks != 0 {
		t.Fatalf("left %d samples and %d checks behind", samples, checks)
	}
}
