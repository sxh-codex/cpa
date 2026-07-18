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

func TestEventFromRecordTracksOpenAICompatibleOAuthJSONAndAPIKey(t *testing.T) {
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
	apiKeyRecord := base
	apiKeyRecord.AuthType = "apikey"
	apiKeyRecord.Provider = "openai-compatible-vast"
	apiKeyRecord.AuthIndex = "1234567890abcdef"
	apiKeyRecord.AuthFileName = "sk-should-not-be-used"
	apiKeyEvent, ok := EventFromRecord(apiKeyRecord)
	if !ok {
		t.Fatal("EventFromRecord(api key) ok = false")
	}
	if apiKeyEvent.AuthType != "apikey" || apiKeyEvent.Provider != "openai-compatible-vast" {
		t.Fatalf("api key event = %+v", apiKeyEvent)
	}
	if apiKeyEvent.DisplayName != "api-key:12345678" || apiKeyEvent.AuthFileName != "api-key:12345678" {
		t.Fatalf("api key display = %q file = %q, want redacted auth index prefix", apiKeyEvent.DisplayName, apiKeyEvent.AuthFileName)
	}
	tests := []struct {
		name   string
		mutate func(*coreusage.Record)
	}{
		{name: "gemini", mutate: func(r *coreusage.Record) { r.Provider = "gemini" }},
		{name: "missing auth index", mutate: func(r *coreusage.Record) { r.AuthIndex = "" }},
		{name: "oauth not json", mutate: func(r *coreusage.Record) { r.AuthFileName = "one.txt" }},
		{name: "unsupported auth type", mutate: func(r *coreusage.Record) { r.AuthType = "session" }},
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
	if want := int64(18_400_000); priority != want {
		t.Fatalf("Price(priority) = %d, want %d", priority, want)
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
	if want := int64(4_600_000); flex != want {
		t.Fatalf("flex cost = %d, want %d", flex, want)
	}
}

func TestPriceGPT55LongContextUsesSub2APITierPricing(t *testing.T) {
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
	priorityLong, ok := Price(event)
	if !ok {
		t.Fatal("Price(priority long) ok = false")
	}
	event.InputTokens = 271999
	priorityShort, ok := Price(event)
	if !ok {
		t.Fatal("Price(priority short) ok = false")
	}
	if priorityLong <= priorityShort {
		t.Fatalf("priority long cost = %d, want above short %d", priorityLong, priorityShort)
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
	if want := int64(10_000_000); got != want {
		t.Fatalf("Price(request default response priority) = %d, want %d", got, want)
	}
}

func TestPricingMissingCountForUnknownModel(t *testing.T) {
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
	})
	account, ok := plugin.Account("unknown")
	if !ok {
		t.Fatal("missing account unknown")
	}
	if account.RequestCount != 1 || account.PricingMissingCount != 1 || account.EstimatedCostNanoUSD != 0 {
		t.Fatalf("unknown account = %+v, want one pricing-missing charged at 0", account)
	}
	if account.InputTokens == 0 || account.TotalTokens == 0 {
		t.Fatalf("unknown tokens were not accumulated: %+v", account)
	}
	if got := plugin.Status().PricingMissingCount; got != 1 {
		t.Fatalf("status PricingMissingCount = %d, want 1", got)
	}
}

func TestPriceGPT56VariantsFromSub2API(t *testing.T) {
	tests := []struct {
		model string
		want  int64
	}{
		{model: "gpt-5.6", want: 5000*500 + 500*400 + 6250*100 + 30000*200},
		{model: "provider/gpt-5.6-sol(high)", want: 5000*500 + 500*400 + 6250*100 + 30000*200},
		{model: "gpt-5.6-terra-20260717", want: 2500*500 + 250*400 + 3125*100 + 15000*200},
		{model: "openai/gpt-5.6-luna-2026-07-17(high)", want: 1000*500 + 100*400 + 1250*100 + 6000*200},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			cost, ok := Price(Event{
				Model:               tt.model,
				ServiceTier:         "default",
				UsageReported:       true,
				TotalTokensReported: true,
				InputTokens:         1000,
				CachedInputTokens:   400,
				CacheCreationTokens: 100,
				OutputTokens:        200,
				ReasoningTokens:     50,
				TotalTokens:         1200,
			})
			if !ok {
				t.Fatal("Price() ok = false")
			}
			if cost != tt.want {
				t.Fatalf("Price() = %d, want %d", cost, tt.want)
			}
		})
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

func TestRecordQuotaSampleBaselineDoesNotCalculate(t *testing.T) {
	plugin := newQuotaSampleTestPlugin(t)
	setQuotaSampleTestCost(t, plugin, "idx", 500*nanoUSD)

	if err := plugin.RecordQuotaSample(context.Background(), QuotaSample{
		AuthIndex:    "idx",
		AuthFileName: "codex-user.json",
		Provider:     "codex",
		AuthType:     "oauth",
		AccountEmail: "user@example.com",
		WindowID:     "weekly",
		UsedPercent:  10,
		SampledAt:    time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("RecordQuotaSample baseline: %v", err)
	}
	account, ok := plugin.Account("idx")
	if !ok {
		t.Fatal("missing account")
	}
	if account.FullQuotaEstimatedNanoUSD != 0 || account.FullQuotaEstimatedUSD != "" {
		t.Fatalf("baseline calculated full quota: %+v", account)
	}
	if account.QuotaSampleWindowID != "weekly" || account.QuotaSampleUsedPercent != 10 || account.QuotaSampleCostNanoUSD != 500*nanoUSD {
		t.Fatalf("baseline fields = %+v", account)
	}
	if account.AuthFileName != "codex-user.json" || account.AccountEmail != "user@example.com" {
		t.Fatalf("identity fields = %+v", account)
	}
}

func TestRecordQuotaSampleSecondSampleCalculatesFullQuota(t *testing.T) {
	plugin := newQuotaSampleTestPlugin(t)
	setQuotaSampleTestCost(t, plugin, "idx", 100*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "monthly", 10)
	setQuotaSampleTestCost(t, plugin, "idx", 600*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "monthly", 20)

	account, ok := plugin.Account("idx")
	if !ok {
		t.Fatal("missing account")
	}
	if want := int64(5000 * nanoUSD); account.FullQuotaEstimatedNanoUSD != want {
		t.Fatalf("FullQuotaEstimatedNanoUSD = %d, want %d", account.FullQuotaEstimatedNanoUSD, want)
	}
	if account.FullQuotaEstimatedUSD != "5000.000000000" {
		t.Fatalf("FullQuotaEstimatedUSD = %q", account.FullQuotaEstimatedUSD)
	}
	if account.QuotaEstimateSamplePercent != 10 || account.QuotaEstimateSampleCostNanoUSD != 500*nanoUSD {
		t.Fatalf("estimate sample fields = %+v", account)
	}
}

func TestRecordQuotaSampleLessThanOnePercentDoesNotCalculate(t *testing.T) {
	plugin := newQuotaSampleTestPlugin(t)
	setQuotaSampleTestCost(t, plugin, "idx", 100*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "weekly", 10)
	setQuotaSampleTestCost(t, plugin, "idx", 150*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "weekly", 10.5)

	account, ok := plugin.Account("idx")
	if !ok {
		t.Fatal("missing account")
	}
	if account.FullQuotaEstimatedNanoUSD != 0 {
		t.Fatalf("FullQuotaEstimatedNanoUSD = %d, want 0", account.FullQuotaEstimatedNanoUSD)
	}
	if account.QuotaSampleUsedPercent != 10.5 || account.QuotaSampleCostNanoUSD != 150*nanoUSD {
		t.Fatalf("baseline not updated after small delta: %+v", account)
	}
}

func TestRecordQuotaSampleMissingCountsPreventCalculation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AccountStats)
	}{
		{name: "usage missing", mutate: func(account *AccountStats) { account.UsageMissingCount = 1 }},
		{name: "pricing missing", mutate: func(account *AccountStats) { account.PricingMissingCount = 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := newQuotaSampleTestPlugin(t)
			setQuotaSampleTestCost(t, plugin, "idx", 100*nanoUSD)
			mustRecordQuotaSample(t, plugin, "idx", "weekly", 10)
			setQuotaSampleTestCost(t, plugin, "idx", 600*nanoUSD)
			plugin.mu.Lock()
			account := plugin.accounts["idx"]
			tt.mutate(&account)
			plugin.accounts["idx"] = account
			plugin.mu.Unlock()
			mustRecordQuotaSample(t, plugin, "idx", "weekly", 20)

			account, ok := plugin.Account("idx")
			if !ok {
				t.Fatal("missing account")
			}
			if account.FullQuotaEstimatedNanoUSD != 0 {
				t.Fatalf("FullQuotaEstimatedNanoUSD = %d, want 0", account.FullQuotaEstimatedNanoUSD)
			}
			if account.QuotaSampleUsedPercent != 20 || account.QuotaSampleCostNanoUSD != 600*nanoUSD {
				t.Fatalf("baseline not updated after missing count: %+v", account)
			}
		})
	}
}

func TestRecordQuotaSampleWindowChangeDoesNotCalculate(t *testing.T) {
	plugin := newQuotaSampleTestPlugin(t)
	setQuotaSampleTestCost(t, plugin, "idx", 100*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "weekly", 10)
	setQuotaSampleTestCost(t, plugin, "idx", 600*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "monthly", 20)

	account, ok := plugin.Account("idx")
	if !ok {
		t.Fatal("missing account")
	}
	if account.FullQuotaEstimatedNanoUSD != 0 {
		t.Fatalf("FullQuotaEstimatedNanoUSD = %d, want 0", account.FullQuotaEstimatedNanoUSD)
	}
	if account.QuotaSampleWindowID != "monthly" || account.QuotaSampleUsedPercent != 20 {
		t.Fatalf("baseline not updated after window change: %+v", account)
	}
}

func TestRecordQuotaSamplePersistsAndReloadsFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "openai-usage.json")
	plugin := NewPersistentUsagePlugin(path)
	t.Cleanup(func() { closePluginForTest(t, plugin) })
	setQuotaSampleTestCost(t, plugin, "idx", 100*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "monthly", 10)
	setQuotaSampleTestCost(t, plugin, "idx", 600*nanoUSD)
	mustRecordQuotaSample(t, plugin, "idx", "monthly", 20)

	reloaded := NewPersistentUsagePlugin(path)
	t.Cleanup(func() { closePluginForTest(t, reloaded) })
	account, ok := reloaded.Account("idx")
	if !ok {
		t.Fatal("missing reloaded account")
	}
	if account.FullQuotaEstimatedNanoUSD != 5000*nanoUSD || account.FullQuotaEstimatedUSD != "5000.000000000" {
		t.Fatalf("reloaded full quota = %+v", account)
	}
	if account.QuotaSampleWindowID != "monthly" || account.QuotaSampleUsedPercent != 20 || account.QuotaSampleCostNanoUSD != 600*nanoUSD {
		t.Fatalf("reloaded sample fields = %+v", account)
	}
	if account.QuotaEstimateSamplePercent != 10 || account.QuotaEstimateSampleCostNanoUSD != 500*nanoUSD || account.QuotaEstimateUpdatedAt == "" {
		t.Fatalf("reloaded estimate fields = %+v", account)
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

const nanoUSD int64 = 1_000_000_000

func newQuotaSampleTestPlugin(t *testing.T) *PersistentUsagePlugin {
	t.Helper()
	plugin := NewPersistentUsagePlugin(filepath.Join(t.TempDir(), "openai-usage.json"))
	t.Cleanup(func() { closePluginForTest(t, plugin) })
	return plugin
}

func closePluginForTest(t *testing.T, plugin *PersistentUsagePlugin) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := plugin.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("close plugin: %v", err)
	}
}

func setQuotaSampleTestCost(t *testing.T, plugin *PersistentUsagePlugin, authIndex string, cost int64) {
	t.Helper()
	plugin.mu.Lock()
	account := plugin.accounts[authIndex]
	account.AuthIndex = authIndex
	account.AuthFileName = "codex-user.json"
	account.DisplayName = "codex-user.json"
	account.Provider = "codex"
	account.AuthType = "oauth"
	account.EstimatedCostNanoUSD = cost
	plugin.accounts[authIndex] = accountForOutput(account)
	plugin.mu.Unlock()
}

func mustRecordQuotaSample(t *testing.T, plugin *PersistentUsagePlugin, authIndex string, windowID string, usedPercent float64) {
	t.Helper()
	if err := plugin.RecordQuotaSample(context.Background(), QuotaSample{
		AuthIndex:    authIndex,
		AuthFileName: "codex-user.json",
		DisplayName:  "codex-user.json",
		Provider:     "codex",
		AuthType:     "oauth",
		AccountEmail: "user@example.com",
		WindowID:     windowID,
		UsedPercent:  usedPercent,
		SampledAt:    time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC).Add(time.Duration(usedPercent) * time.Second),
	}); err != nil {
		t.Fatalf("RecordQuotaSample: %v", err)
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
