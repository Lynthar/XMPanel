// Package monitor samples the registered backends in the background and
// records the periodic checks (TLS expiry, DNS, well-known, federation) that
// the Overview page shows. The public /health endpoint answers from the
// snapshot this package keeps, never by probing on the request path.
package monitor

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmpanel/xmpanel/internal/adapter/registry"
	"github.com/xmpanel/xmpanel/internal/config"
	"github.com/xmpanel/xmpanel/internal/store"

	"go.uber.org/zap"
)

const (
	// probeTimeout bounds every network operation the monitor performs, so a
	// hung backend costs one slot for five seconds and nothing more.
	probeTimeout = 5 * time.Second
	// maxInFlight is the width of the fan-out; the scheduler itself stays a
	// single goroutine.
	maxInFlight = 4
	// cleanupInterval is how often expired samples are deleted.
	cleanupInterval = 24 * time.Hour
)

// Health is what /health reports: aggregate counts only, never per-server
// detail, because that endpoint is public.
type Health struct {
	OK     int
	Failed int
}

// Monitor owns the background loops. Construct it with New, start the loops
// with Start, and release them with Close.
type Monitor struct {
	db       *store.DB
	adapters *registry.Registry
	cfg      config.MonitorConfig
	logger   *zap.Logger

	health   atomic.Pointer[Health]
	checking atomic.Bool
	checkWG  sync.WaitGroup
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(db *store.DB, adapters *registry.Registry, cfg config.MonitorConfig, logger *zap.Logger) *Monitor {
	return &Monitor{db: db, adapters: adapters, cfg: cfg, logger: logger}
}

// Start launches the scheduler. It returns immediately; the first sample and
// check rounds run at once rather than after a full interval, so a freshly
// started panel does not report an empty /health for a minute.
func (m *Monitor) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.done = make(chan struct{})
	go m.run(ctx)
}

// Close stops the scheduler and waits for the round in flight to finish.
// It is safe to call on a Monitor that was never started.
func (m *Monitor) Close() {
	if m.cancel == nil {
		return
	}
	m.cancel()
	<-m.done
}

// Health returns the last completed sample round, or nil before the first one
// finishes. Callers must not mutate the result.
func (m *Monitor) Health() *Health {
	return m.health.Load()
}

// run is the scheduler goroutine. Sample rounds run on it directly, so two of
// them never overlap; check rounds are detached because a round of them costs
// seconds per unresponsive server and /health is only as fresh as the last
// sample.
func (m *Monitor) run(ctx context.Context) {
	defer close(m.done)
	defer m.checkWG.Wait()

	sampleTick := time.NewTicker(m.cfg.SampleInterval)
	defer sampleTick.Stop()
	checkTick := time.NewTicker(m.cfg.CheckInterval)
	defer checkTick.Stop()
	cleanupTick := time.NewTicker(cleanupInterval)
	defer cleanupTick.Stop()

	m.sampleRound(ctx)
	m.startCheckRound(ctx)
	m.cleanup(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sampleTick.C:
			m.sampleRound(ctx)
		case <-checkTick.C:
			m.startCheckRound(ctx)
		case <-cleanupTick.C:
			m.cleanup(ctx)
		}
	}
}

// startCheckRound runs the checks off the scheduler. A round that outlives its
// interval makes the next tick skip rather than stack a second round on top of
// it, which on a domain whose DNS never answers would otherwise multiply.
func (m *Monitor) startCheckRound(ctx context.Context) {
	if !m.checking.CompareAndSwap(false, true) {
		m.logger.Warn("monitor: previous check round still running, skipping this one")
		return
	}
	m.checkWG.Add(1)
	go func() {
		defer m.checkWG.Done()
		defer m.checking.Store(false)
		m.checkRound(ctx)
	}()
}

// forEachServer runs fn for every enabled server with at most maxInFlight in
// flight, and waits for all of them.
func (m *Monitor) forEachServer(ctx context.Context, what string, fn func(context.Context, serverRow)) {
	servers, err := enabledServers(ctx, m.db)
	if err != nil {
		m.logger.Warn("monitor: failed to list servers", zap.String("round", what), zap.Error(err))
		return
	}
	sem := make(chan struct{}, maxInFlight)
	var wg sync.WaitGroup
	for _, s := range servers {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(s serverRow) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(ctx, s)
		}(s)
	}
	wg.Wait()
}

// sampleRound probes every enabled server once and records the result. The
// /health snapshot is replaced only after the whole round finishes, so the
// public endpoint never shows a half-counted round.
func (m *Monitor) sampleRound(ctx context.Context) {
	ts := time.Now().UTC().Truncate(time.Millisecond)
	var mu sync.Mutex
	var health Health
	samples := make([]sample, 0, 8)

	m.forEachServer(ctx, "sample", func(ctx context.Context, s serverRow) {
		result := m.sampleServer(ctx, s.id)
		mu.Lock()
		defer mu.Unlock()
		if result.ok {
			health.OK++
		} else {
			health.Failed++
		}
		samples = append(samples, result)
	})
	if ctx.Err() != nil {
		return
	}

	m.health.Store(&health)
	if failed := insertSamples(ctx, m.db, ts, samples); failed > 0 {
		m.logger.Warn("monitor: failed to store samples", zap.Int("rows", failed))
	}
}

// sampleServer measures one probe round trip and reads the counters. Latency
// covers the probe alone: the registry answers from cache, and on the very
// first call its own probe would otherwise be counted twice.
func (m *Monitor) sampleServer(ctx context.Context, id int64) sample {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	result := sample{serverID: id}
	a, _, err := m.adapters.Get(ctx, id)
	if err != nil {
		return result
	}
	start := time.Now()
	if _, err := a.Probe(ctx); err != nil {
		return result
	}
	ms := int(time.Since(start).Milliseconds())
	result.ok, result.latencyMS = true, &ms

	// Counters are a bonus: a backend that answers the probe but not the stats
	// call is up, it just reports less.
	if stats, err := a.Stats(ctx); err == nil {
		result.registeredUsers = stats.RegisteredUsers
		result.onlineUsers = stats.OnlineUsers
		result.activeSessions = stats.ActiveSessions
		result.rooms = stats.Rooms
	}
	return result
}

// checkRound runs the periodic checks for every enabled server and stores the
// latest result per kind.
func (m *Monitor) checkRound(ctx context.Context) {
	m.forEachServer(ctx, "check", func(ctx context.Context, s serverRow) {
		for _, result := range m.checkServer(ctx, s) {
			if ctx.Err() != nil {
				return
			}
			if err := storeCheck(ctx, m.db, s.id, result); err != nil {
				m.logger.Warn("monitor: failed to store check",
					zap.Int64("server_id", s.id), zap.String("kind", result.Kind), zap.Error(err))
			}
		}
	})
}

func (m *Monitor) cleanup(ctx context.Context) {
	if err := deleteExpired(ctx, m.db, m.cfg.Retention); err != nil {
		m.logger.Warn("monitor: failed to delete expired samples", zap.Error(err))
	}
}
