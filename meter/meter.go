package meter

import (
	"context"
	"sync"
	"time"
)

type contextKey int

const ctxMeter contextKey = 0

// TODO: Infrastructure cost allocation (platform-side, not SDK)
// - Cost data (Lambda, ECS, S3, R2, RDS, ElastiCache) is visible only to
//   the module creator (user/org) by default.
// - Module creators can grant read access to specific users/orgs via a
//   permission system (e.g., cost_viewer role on the module resource).
// - Platform aggregates AWS costs per app/module using resource tags and
//   usage metrics collected by the Meter.
// - Cost dashboard: platform reads Cost Explorer + CloudWatch + S3 inventory,
//   joins with meter data for DB/cache cost allocation.

// UsageEntry is a single usage metric.
type UsageEntry struct {
	AppID    string  `json:"app_id"`
	ModuleID string  `json:"module_id"`
	Metric   string  `json:"metric"`
	Value    float64 `json:"value"`
	Time     int64   `json:"time"` // unix ms
}

// UsageSink receives batched usage entries.
// Platform implements this to store/forward metrics.
type UsageSink interface {
	FlushUsage(ctx context.Context, entries []UsageEntry) error
}

// Meter collects usage metrics and flushes them periodically or on demand.
// Safe for concurrent use.
type Meter struct {
	sink      UsageSink
	appID     string
	moduleID  string
	mu        sync.Mutex
	entries   []UsageEntry
	interval  time.Duration
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a meter that flushes to sink every interval.
// appID and moduleID are stamped on every entry.
// Use interval=0 to disable auto-flush (manual Flush() only).
//
//	m := meter.New(sink, appID, moduleID, 30*time.Second)
//	defer m.Close()
func New(sink UsageSink, appID, moduleID string, interval time.Duration) *Meter {
	m := &Meter{
		sink:     sink,
		appID:    appID,
		moduleID: moduleID,
		entries:  make([]UsageEntry, 0, 64),
		interval: interval,
		done:     make(chan struct{}),
	}
	if interval > 0 {
		go m.autoFlush()
	}
	return m
}

// WithContext adds a meter to the context.
// Storage operations (WithSchema, CacheClient, FileClient) auto-track
// usage if a meter is present in the context.
//
//	ctx = meter.WithContext(ctx, m)
func WithContext(ctx context.Context, m *Meter) context.Context {
	return context.WithValue(ctx, ctxMeter, m)
}

// FromContext retrieves the meter from context. Returns nil if none set.
func FromContext(ctx context.Context) *Meter {
	if m, ok := ctx.Value(ctxMeter).(*Meter); ok {
		return m
	}
	return nil
}

// Track records a usage metric. Appends to buffer, does not block on I/O.
//
//	m.Track("transcode_minutes", 12.5)
//	m.Track("login", 1)
func (m *Meter) Track(metric string, value float64) {
	entry := UsageEntry{
		AppID:    m.appID,
		ModuleID: m.moduleID,
		Metric:   metric,
		Value:    value,
		Time:     time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.entries = append(m.entries, entry)
	m.mu.Unlock()
}

// Flush sends all buffered entries to the sink and clears the buffer.
func (m *Meter) Flush(ctx context.Context) error {
	m.mu.Lock()
	if len(m.entries) == 0 {
		m.mu.Unlock()
		return nil
	}
	batch := m.entries
	m.entries = make([]UsageEntry, 0, 64)
	m.mu.Unlock()

	return m.sink.FlushUsage(ctx, batch)
}

// Close stops auto-flush and flushes remaining entries. Safe to call multiple times.
func (m *Meter) Close() error {
	m.closeOnce.Do(func() { close(m.done) })
	return m.Flush(context.Background())
}

func (m *Meter) autoFlush() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.Flush(context.Background()) //nolint:errcheck
		case <-m.done:
			return
		}
	}
}
