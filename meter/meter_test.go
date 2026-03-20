package meter_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mirrorstack-ai/app-module-sdk/meter"
)

// --- Mock Sink ---

type mockSink struct {
	mu      sync.Mutex
	entries []meter.UsageEntry
}

func (s *mockSink) FlushUsage(_ context.Context, entries []meter.UsageEntry) error {
	s.mu.Lock()
	s.entries = append(s.entries, entries...)
	s.mu.Unlock()
	return nil
}

func (s *mockSink) getEntries() []meter.UsageEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]meter.UsageEntry, len(s.entries))
	copy(cp, s.entries)
	return cp
}

// --- Track ---

func TestMeter_Track(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 0)
	defer m.Close()

	m.Track("transcode_minutes", 12.5)
	m.Track("login", 1)

	if err := m.Flush(context.Background()); err != nil {
		t.Fatalf("flush error: %v", err)
	}

	entries := sink.getEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].AppID != "app-1" {
		t.Errorf("app_id: got %q", entries[0].AppID)
	}
	if entries[0].ModuleID != "video" {
		t.Errorf("module_id: got %q", entries[0].ModuleID)
	}
	if entries[0].Metric != "transcode_minutes" {
		t.Errorf("metric: got %q", entries[0].Metric)
	}
	if entries[0].Value != 12.5 {
		t.Errorf("value: got %f", entries[0].Value)
	}
	if entries[1].Metric != "login" {
		t.Errorf("metric: got %q", entries[1].Metric)
	}
}

func TestMeter_FlushEmpty(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 0)
	defer m.Close()

	if err := m.Flush(context.Background()); err != nil {
		t.Fatalf("flush empty should not error: %v", err)
	}
	if len(sink.getEntries()) != 0 {
		t.Error("no entries should be flushed")
	}
}

func TestMeter_AutoFlush(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 50*time.Millisecond)
	defer m.Close()

	m.Track("test", 1)

	time.Sleep(150 * time.Millisecond)

	entries := sink.getEntries()
	if len(entries) != 1 {
		t.Errorf("expected 1 auto-flushed entry, got %d", len(entries))
	}
}

func TestMeter_Close_FlushesRemaining(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 0)

	m.Track("final", 99)
	m.Close()

	entries := sink.getEntries()
	if len(entries) != 1 {
		t.Fatalf("close should flush remaining: got %d entries", len(entries))
	}
	if entries[0].Metric != "final" {
		t.Errorf("metric: got %q", entries[0].Metric)
	}
}

func TestMeter_ConcurrentTrack(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 0)
	defer m.Close()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.Track("concurrent", float64(n))
		}(i)
	}
	wg.Wait()

	m.Flush(context.Background())

	entries := sink.getEntries()
	if len(entries) != 100 {
		t.Errorf("expected 100 entries, got %d", len(entries))
	}
}

// --- Context ---

func TestWithContext_FromContext(t *testing.T) {
	sink := &mockSink{}
	m := meter.New(sink, "app-1", "video", 0)
	defer m.Close()

	ctx := meter.WithContext(context.Background(), m)

	got := meter.FromContext(ctx)
	if got != m {
		t.Error("meter not found in context")
	}
}

func TestFromContext_Nil(t *testing.T) {
	got := meter.FromContext(context.Background())
	if got != nil {
		t.Error("should return nil for bare context")
	}
}
