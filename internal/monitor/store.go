package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter"
	"github.com/xmpanel/xmpanel/internal/store"
)

// serverRow is what the monitor needs to check a server: the protocol decides
// which checks apply, the domain is the identity they are run against.
type serverRow struct {
	id       int64
	protocol adapter.Protocol
	domain   string
}

// sample is one probe result, before it becomes a row.
type sample struct {
	serverID        int64
	ok              bool
	latencyMS       *int
	registeredUsers *int
	onlineUsers     *int
	activeSessions  *int
	rooms           *int
}

// Point is one bucket of the sampled history. OKRatio is the share of samples
// in the bucket that reached the server; the counters are the last reading in
// the bucket, so a curve follows the value rather than an average of two ticks.
type Point struct {
	TS              time.Time `json:"ts"`
	OKRatio         float64   `json:"ok_ratio"`
	LatencyMS       *int      `json:"latency_ms"`
	RegisteredUsers *int      `json:"registered_users"`
	OnlineUsers     *int      `json:"online_users"`
	ActiveSessions  *int      `json:"active_sessions"`
	Rooms           *int      `json:"rooms"`
}

// Series is a window of sampled history. From and To bound the x axis even
// where no sample exists, so a gap reads as a gap.
type Series struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int       `json:"bucket_seconds"`
	Points        []Point   `json:"points"`
}

// Check is the latest result of one periodic check.
type Check struct {
	Kind   string      `json:"kind"`
	TS     time.Time   `json:"ts"`
	Status string      `json:"status"`
	Detail CheckDetail `json:"detail"`
}

func enabledServers(ctx context.Context, db *store.DB) ([]serverRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, protocol, domain FROM servers WHERE enabled = TRUE ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []serverRow
	for rows.Next() {
		var s serverRow
		if err := rows.Scan(&s.id, &s.protocol, &s.domain); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// insertSamples writes one row per sample and returns how many failed. A
// duplicate timestamp means the same tick was already recorded, which a clock
// step backwards can produce; it is not an error worth failing the round.
func insertSamples(ctx context.Context, db *store.DB, ts time.Time, samples []sample) int {
	var failed int
	for _, s := range samples {
		_, err := db.ExecContext(ctx,
			`INSERT INTO server_samples
			 (server_id, ts, ok, latency_ms, registered_users, online_users, active_sessions, rooms)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 ON CONFLICT (server_id, ts) DO NOTHING`,
			s.serverID, ts, s.ok, nullInt(s.latencyMS), nullInt(s.registeredUsers),
			nullInt(s.onlineUsers), nullInt(s.activeSessions), nullInt(s.rooms))
		if err != nil {
			failed++
		}
	}
	return failed
}

func storeCheck(ctx context.Context, db *store.DB, serverID int64, result Check) error {
	detail, err := json.Marshal(result.Detail)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO server_checks (server_id, kind, ts, status, detail)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (server_id, kind) DO UPDATE SET ts = EXCLUDED.ts, status = EXCLUDED.status, detail = EXCLUDED.detail`,
		serverID, result.Kind, result.TS, result.Status, string(detail))
	return err
}

func deleteExpired(ctx context.Context, db *store.DB, retention time.Duration) error {
	cutoff := time.Now().UTC().Add(-retention)
	if _, err := db.ExecContext(ctx, `DELETE FROM server_samples WHERE ts < $1`, cutoff); err != nil {
		return err
	}
	// A check row is refreshed every check interval, so one older than the
	// retention window belongs to a kind that no longer applies to the server.
	_, err := db.ExecContext(ctx, `DELETE FROM server_checks WHERE ts < $1`, cutoff)
	return err
}

// maxPoints caps how many buckets a window is aggregated into, so thirty days
// of minute samples reach the browser as a few hundred points.
const maxPoints = 360

// QuerySamples returns the sampled history of one server over the window
// ending now, bucketed so the result stays under maxPoints.
func QuerySamples(ctx context.Context, db *store.DB, serverID int64, window, sampleInterval time.Duration) (*Series, error) {
	bucket := window / maxPoints
	if bucket < sampleInterval {
		bucket = sampleInterval
	}
	bucket = bucket.Round(time.Second)
	if bucket < time.Second {
		bucket = time.Second
	}

	to := time.Now().UTC()
	from := to.Add(-window)
	series := &Series{From: from, To: to, BucketSeconds: int(bucket.Seconds()), Points: []Point{}}

	// Buckets are aligned to a fixed epoch rather than to `from`, so repeated
	// requests return the same bucket timestamps and a polling chart does not
	// slide sideways between refreshes.
	rows, err := db.QueryContext(ctx,
		`SELECT date_bin($1::interval, ts, TIMESTAMP '2000-01-01 00:00:00') AS bucket,
		        avg(CASE WHEN ok THEN 1.0 ELSE 0.0 END),
		        avg(latency_ms) FILTER (WHERE ok),
		        (array_agg(registered_users ORDER BY ts DESC) FILTER (WHERE registered_users IS NOT NULL))[1],
		        (array_agg(online_users     ORDER BY ts DESC) FILTER (WHERE online_users     IS NOT NULL))[1],
		        (array_agg(active_sessions  ORDER BY ts DESC) FILTER (WHERE active_sessions  IS NOT NULL))[1],
		        (array_agg(rooms            ORDER BY ts DESC) FILTER (WHERE rooms            IS NOT NULL))[1]
		 FROM server_samples
		 WHERE server_id = $2 AND ts >= $3 AND ts <= $4
		 GROUP BY bucket ORDER BY bucket`,
		fmt.Sprintf("%d seconds", int(bucket.Seconds())), serverID, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var p Point
		var latency sql.NullFloat64
		if err := rows.Scan(&p.TS, &p.OKRatio, &latency,
			&p.RegisteredUsers, &p.OnlineUsers, &p.ActiveSessions, &p.Rooms); err != nil {
			return nil, err
		}
		if latency.Valid {
			ms := int(latency.Float64 + 0.5)
			p.LatencyMS = &ms
		}
		series.Points = append(series.Points, p)
	}
	return series, rows.Err()
}

// QueryChecks returns the latest result of every check recorded for a server.
func QueryChecks(ctx context.Context, db *store.DB, serverID int64) ([]Check, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT kind, ts, status, detail FROM server_checks WHERE server_id = $1 ORDER BY kind`, serverID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []Check{}
	for rows.Next() {
		var c Check
		var detail []byte
		if err := rows.Scan(&c.Kind, &c.TS, &c.Status, &detail); err != nil {
			return nil, err
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &c.Detail); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
