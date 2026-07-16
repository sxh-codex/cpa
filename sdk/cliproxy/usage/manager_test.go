package usage

import (
	"context"
	"sync"
	"testing"
	"time"
)

type testUsagePlugin struct {
	mu         sync.Mutex
	requestIDs []string
	ch         chan string
}

func newTestUsagePlugin(buffer int) *testUsagePlugin {
	return &testUsagePlugin{ch: make(chan string, buffer)}
}

func (p *testUsagePlugin) HandleUsage(ctx context.Context, record Record) {
	p.mu.Lock()
	p.requestIDs = append(p.requestIDs, record.RequestID)
	p.mu.Unlock()

	select {
	case p.ch <- record.RequestID:
	default:
	}
}

func (p *testUsagePlugin) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requestIDs)
}

func (p *testUsagePlugin) contains(requestID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, existing := range p.requestIDs {
		if existing == requestID {
			return true
		}
	}
	return false
}

func waitForUsageRecord(t *testing.T, p *testUsagePlugin, requestID string) {
	t.Helper()

	timeout := time.After(time.Second)
	for {
		select {
		case got := <-p.ch:
			if got == requestID {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for usage record %q", requestID)
		}
	}
}

func stopUsageManagerForTest(t *testing.T, m *Manager) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.StopAndWait(ctx); err != nil {
		t.Fatalf("failed to stop usage manager: %v", err)
	}
}

func TestManagerRestartsAfterStopAndWait(t *testing.T) {
	m := NewManager(4)
	plugin := newTestUsagePlugin(2)
	m.Register(plugin)

	m.Start(context.Background())
	m.Publish(context.Background(), Record{RequestID: "first"})
	waitForUsageRecord(t, plugin, "first")
	stopUsageManagerForTest(t, m)

	m.Start(context.Background())
	m.Publish(context.Background(), Record{RequestID: "second"})
	waitForUsageRecord(t, plugin, "second")
	stopUsageManagerForTest(t, m)

	if plugin.count() != 2 {
		t.Fatalf("handled records = %d, want 2", plugin.count())
	}
	if !plugin.contains("first") || !plugin.contains("second") {
		t.Fatalf("handled records = %#v, want first and second", plugin.requestIDs)
	}
}

func TestRegisterNamedReplacesPluginAfterRestart(t *testing.T) {
	m := NewManager(4)
	first := newTestUsagePlugin(1)
	second := newTestUsagePlugin(1)

	m.RegisterNamed("openai-usage-test", first)
	m.Start(context.Background())
	m.Publish(context.Background(), Record{RequestID: "first"})
	waitForUsageRecord(t, first, "first")
	stopUsageManagerForTest(t, m)

	m.RegisterNamed("openai-usage-test", second)
	m.Start(context.Background())
	m.Publish(context.Background(), Record{RequestID: "second"})
	waitForUsageRecord(t, second, "second")
	stopUsageManagerForTest(t, m)

	if first.count() != 1 {
		t.Fatalf("first plugin handled %d records, want 1", first.count())
	}
	if second.count() != 1 {
		t.Fatalf("second plugin handled %d records, want 1", second.count())
	}
	if first.contains("second") {
		t.Fatalf("first plugin handled record after replacement")
	}
}

func TestUnregisterNamedPluginStopsDispatch(t *testing.T) {
	m := NewManager(4)
	plugin := newTestUsagePlugin(1)

	m.RegisterNamed("openai-usage-test", plugin)
	m.Start(context.Background())
	m.Publish(context.Background(), Record{RequestID: "first"})
	waitForUsageRecord(t, plugin, "first")

	m.UnregisterNamed("openai-usage-test")
	m.Publish(context.Background(), Record{RequestID: "second"})
	stopUsageManagerForTest(t, m)

	if plugin.count() != 1 {
		t.Fatalf("plugin handled %d records after unregister, want 1", plugin.count())
	}
	if plugin.contains("second") {
		t.Fatalf("plugin handled record after unregister")
	}
}
