package openaiusage

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestEventFromRecordFiltersOnlyOpenAIOAuthJSON(t *testing.T) {
	base := coreusage.Record{
		Provider:      "codex",
		AuthType:      "oauth",
		AuthIndex:     "auth-1",
		AuthFileName:  "one.json",
		AccountEmail:  "user@example.com",
		Model:         "gpt-5.5",
		UsageReported: true,
		Detail: coreusage.Detail{
			InputTokens: 100,
		},
	}
	if event, ok := EventFromRecord(base); !ok || event.AuthIndex != "auth-1" || event.AuthFileName != "one.json" {
		t.Fatalf("EventFromRecord(base) = %+v, %v", event, ok)
	}
	tests := []struct {
		name   string
		mutate func(*coreusage.Record)
	}{
		{name: "api key", mutate: func(r *coreusage.Record) { r.AuthType = "apikey" }},
		{name: "gemini", mutate: func(r *coreusage.Record) { r.Provider = "gemini" }},
		{name: "openai compatibility", mutate: func(r *coreusage.Record) { r.Provider = "openai-compatibility" }},
		{name: "missing auth index", mutate: func(r *coreusage.Record) { r.AuthIndex = "" }},
		{name: "not json", mutate: func(r *coreusage.Record) { r.AuthFileName = "one.txt" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := base
			tt.mutate(&record)
			if event, ok := EventFromRecord(record); ok {
				t.Fatalf("EventFromRecord() = %+v, true; want false", event)
			}
		})
	}
}

func TestEventFromRecordNormalizesAuthFilePath(t *testing.T) {
	record := coreusage.Record{
		Provider:      "codex",
		AuthType:      "oauth",
		AuthIndex:     "auth-1",
		AuthFileName:  filepath.Join(t.TempDir(), "codex-user.json"),
		Model:         "gpt-5.5",
		UsageReported: true,
		Detail:        coreusage.Detail{InputTokens: 100},
	}

	event, ok := EventFromRecord(record)
	if !ok {
		t.Fatal("EventFromRecord() ok = false")
	}
	if event.AuthFileName != "codex-user.json" {
		t.Fatalf("AuthFileName = %q, want codex-user.json", event.AuthFileName)
	}
}

func TestApplyAggregatesByAuthIndexNotEmail(t *testing.T) {
	plugin := NewPersistentUsagePlugin(filepath.Join(t.TempDir(), "openai-usage.json"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plugin.Close(ctx)
	}()
	now := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	plugin.apply([]Event{
		{
			AuthIndex:           "idx-a",
			AuthFileName:        "a.json",
			AccountEmail:        "same@example.com",
			Model:               "gpt-5.5",
			RequestedAt:         now,
			UsageReported:       true,
			TotalTokensReported: true,
			InputTokens:         1000,
			CachedInputTokens:   400,
			CacheCreationTokens: 100,
			OutputTokens:        200,
			ReasoningTokens:     50,
			TotalTokens:         1200,
		},
		{
			AuthIndex:     "idx-b",
			AuthFileName:  "b.json",
			AccountEmail:  "same@example.com",
			Model:         "gpt-5.5",
			RequestedAt:   now.Add(time.Second),
			UsageReported: false,
		},
	})
	accounts := plugin.Accounts()
	if len(accounts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accounts))
	}
	first, ok := plugin.Account("idx-a")
	if !ok {
		t.Fatal("missing idx-a")
	}
	if first.InputTokens != 1000 || first.CachedInputTokens != 400 || first.OutputTokens != 200 || first.ReasoningTokens != 50 || first.TotalTokens != 1200 {
		t.Fatalf("idx-a tokens = %+v", first)
	}
	second, ok := plugin.Account("idx-b")
	if !ok {
		t.Fatal("missing idx-b")
	}
	if second.UsageMissingCount != 1 || second.RequestCount != 1 {
		t.Fatalf("idx-b = %+v, want one missing request", second)
	}
}

func TestPriceGPT55CachedReasoningAndTiers(t *testing.T) {
	event := Event{
		Model:               "openai/gpt-5.5-2026-07-14(high)",
		ServiceTier:         "default",
		UsageReported:       true,
		TotalTokensReported: true,
		InputTokens:         1000,
		CachedInputTokens:   400,
		CacheCreationTokens: 100,
		OutputTokens:        200,
		ReasoningTokens:     50,
		TotalTokens:         1200,
	}
	if got := CanonicalModel(event.Model); got != "gpt-5.5" {
		t.Fatalf("CanonicalModel() = %q, want gpt-5.5", got)
	}
	cost, ok := Price(event)
	if !ok {
		t.Fatal("Price() ok = false")
	}
	// regular input 500 * 5000 + cached 400 * 500 + cache creation 100 * 5000 + output 200 * 30000.
	if want := int64(9_200_000); cost != want {
		t.Fatalf("Price(default) = %d, want %d", cost, want)
	}
	event.ServiceTier = "priority"
	priority, ok := Price(event)
	if !ok {
		t.Fatal("Price(priority) ok = false")
	}
	if priority <= cost {
		t.Fatalf("priority cost = %d, want above default %d", priority, cost)
	}
	event.ServiceTier = "batch"
	batch, ok := Price(event)
	if !ok {
		t.Fatal("Price(batch) ok = false")
	}
	if batch >= cost {
		t.Fatalf("batch cost = %d, want below default %d", batch, cost)
	}
	event.ServiceTier = "flex"
	flex, ok := Price(event)
	if !ok {
		t.Fatal("Price(flex) ok = false")
	}
	if flex != batch {
		t.Fatalf("flex cost = %d, want batch cost %d", flex, batch)
	}
}

func TestPriceGPT55LongContextUsesTierSpecificLongPrice(t *testing.T) {
	event := Event{
		Model:             "gpt-5.5",
		ServiceTier:       "flex",
		UsageReported:     true,
		InputTokens:       272000,
		CachedInputTokens: 1000,
		OutputTokens:      10,
	}
	longCost, ok := Price(event)
	if !ok {
		t.Fatal("Price(long flex) ok = false")
	}
	event.InputTokens = 271999
	shortCost, ok := Price(event)
	if !ok {
		t.Fatal("Price(short flex) ok = false")
	}
	if longCost <= shortCost {
		t.Fatalf("long flex cost = %d, want above short %d", longCost, shortCost)
	}
	event.InputTokens = 272000
	event.ServiceTier = "priority"
	if priorityLong, ok := Price(event); ok {
		t.Fatalf("Price(priority long) = %d, true; want pricing missing", priorityLong)
	}
}

func TestPriceUsesResponseServiceTierWhenPresent(t *testing.T) {
	event := Event{
		Model:               "gpt-5.5",
		ServiceTier:         "priority",
		ResponseServiceTier: "default",
		UsageReported:       true,
		InputTokens:         1000,
	}
	got, ok := Price(event)
	if !ok {
		t.Fatal("Price(request priority response default) ok = false")
	}
	if want := int64(5_000_000); got != want {
		t.Fatalf("Price(request priority response default) = %d, want %d", got, want)
	}
	event.ServiceTier = "default"
	event.ResponseServiceTier = "priority"
	got, ok = Price(event)
	if !ok {
		t.Fatal("Price(request default response priority) ok = false")
	}
	if want := int64(12_500_000); got != want {
		t.Fatalf("Price(request default response priority) = %d, want %d", got, want)
	}
}

func TestPricingMissingCountForUnknownModelAndPriorityLongContext(t *testing.T) {
	plugin := NewPersistentUsagePlugin(filepath.Join(t.TempDir(), "openai-usage.json"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plugin.Close(ctx)
	}()
	plugin.apply([]Event{
		{
			AuthIndex:     "unknown",
			AuthFileName:  "unknown.json",
			Model:         "unknown-model",
			UsageReported: true,
			InputTokens:   10,
			OutputTokens:  20,
		},
		{
			AuthIndex:     "priority-long",
			AuthFileName:  "priority-long.json",
			Model:         "gpt-5.5",
			ServiceTier:   "priority",
			UsageReported: true,
			InputTokens:   272000,
			OutputTokens:  1,
		},
	})
	for _, authIndex := range []string{"unknown", "priority-long"} {
		account, ok := plugin.Account(authIndex)
		if !ok {
			t.Fatalf("missing account %s", authIndex)
		}
		if account.RequestCount != 1 || account.PricingMissingCount != 1 || account.EstimatedCostNanoUSD != 0 {
			t.Fatalf("%s account = %+v, want one pricing-missing charged at 0", authIndex, account)
		}
		if account.InputTokens == 0 || account.TotalTokens == 0 {
			t.Fatalf("%s tokens were not accumulated: %+v", authIndex, account)
		}
	}
	if got := plugin.Status().PricingMissingCount; got != 2 {
		t.Fatalf("status PricingMissingCount = %d, want 2", got)
	}
}

func TestJSONPersistenceAtomicLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "openai-usage.json")
	plugin := NewPersistentUsagePlugin(path)
	plugin.apply([]Event{{
		AuthIndex:     "idx",
		AuthFileName:  "idx.json",
		Model:         "gpt-5.5",
		UsageReported: true,
		InputTokens:   1,
		OutputTokens:  1,
	}})
	if err := plugin.save(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
	reloaded := NewPersistentUsagePlugin(path)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plugin.Close(ctx)
		_ = reloaded.Close(ctx)
	}()
	account, ok := reloaded.Account("idx")
	if !ok || account.RequestCount != 1 || account.TotalTokens != 2 {
		t.Fatalf("reloaded account = %+v, %v", account, ok)
	}
}

func TestJSONLoadMissingPricingMissingCountDefaultsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "openai-usage.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := []byte(`{
  "version": 1,
  "accounts": {
    "idx": {
      "auth_index": "idx",
      "auth_file_name": "idx.json",
      "request_count": 2,
      "input_tokens": 3,
      "estimated_cost_nano_usd": 4
    }
  }
}`)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write old usage json: %v", err)
	}
	plugin := NewPersistentUsagePlugin(path)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plugin.Close(ctx)
	}()
	account, ok := plugin.Account("idx")
	if !ok {
		t.Fatal("missing old account")
	}
	if account.PricingMissingCount != 0 {
		t.Fatalf("PricingMissingCount = %d, want 0", account.PricingMissingCount)
	}
}

func TestPersistRetryKeepsBatch(t *testing.T) {
	plugin := NewPersistentUsagePlugin(filepath.Join(t.TempDir(), "openai-usage.json"))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = plugin.Close(ctx)
	}()
	failures := 0
	plugin.saveFn = func(context.Context) error {
		if failures < 2 {
			failures++
			return errors.New("temporary failure")
		}
		return nil
	}
	plugin.flushWithRetry(context.Background(), []Event{{
		AuthIndex:     "idx",
		AuthFileName:  "idx.json",
		Model:         "gpt-5.5",
		UsageReported: true,
		InputTokens:   3,
		OutputTokens:  4,
	}})
	account, ok := plugin.Account("idx")
	if !ok || account.RequestCount != 1 || account.TotalTokens != 7 {
		t.Fatalf("account after retry = %+v, %v", account, ok)
	}
	if failures != 2 {
		t.Fatalf("failures = %d, want 2", failures)
	}
}

func TestCloseCancelsPermanentPersistFailure(t *testing.T) {
	plugin := NewPersistentUsagePlugin(filepath.Join(t.TempDir(), "openai-usage.json"))
	plugin.saveFn = func(context.Context) error {
		return errors.New("permanent failure")
	}
	record := coreusage.Record{
		Provider:      "codex",
		AuthType:      "oauth",
		AuthIndex:     "idx",
		AuthFileName:  "idx.json",
		UsageReported: true,
		Detail:        coreusage.Detail{InputTokens: 1},
	}
	plugin.HandleUsage(context.Background(), record)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := plugin.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v, want DeadlineExceeded", err)
	}
	select {
	case <-plugin.done:
	default:
		t.Fatal("worker did not exit after Close timeout")
	}
}

func TestQueueFullDropsWithoutBlocking(t *testing.T) {
	plugin := &PersistentUsagePlugin{
		events:   make(chan Event, 1),
		done:     make(chan struct{}),
		accounts: map[string]AccountStats{},
	}
	plugin.events <- Event{AuthIndex: "filled"}
	record := coreusage.Record{
		Provider:      "codex",
		AuthType:      "oauth",
		AuthIndex:     "idx",
		AuthFileName:  "idx.json",
		UsageReported: true,
		Detail:        coreusage.Detail{InputTokens: 1},
	}
	plugin.HandleUsage(context.Background(), record)
	if got := plugin.droppedEvents.Load(); got != 1 {
		t.Fatalf("droppedEvents = %d, want 1", got)
	}
	close(plugin.events)
	close(plugin.done)
}

func TestUsageMissingDoesNotCharge(t *testing.T) {
	event := Event{
		Model:               "gpt-5.5",
		UsageReported:       false,
		TotalTokensReported: false,
		InputTokens:         0,
		OutputTokens:        0,
	}
	if cost, ok := Price(event); ok || cost != 0 {
		t.Fatalf("Price(missing) = %d, %v; want 0,false", cost, ok)
	}
	record := coreusage.Record{
		Provider:     "codex",
		AuthType:     "oauth",
		AuthIndex:    "idx",
		AuthFileName: "idx.json",
		StatusCode:   http.StatusOK,
		Detail:       coreusage.Detail{},
	}
	parsed, ok := EventFromRecord(record)
	if !ok {
		t.Fatal("EventFromRecord() ok = false")
	}
	if parsed.UsageReported {
		t.Fatal("UsageReported = true, want false")
	}
}
