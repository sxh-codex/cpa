package openaiusage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	PluginName     = "openai-oauth-json-usage"
	QueueLimit     = 4096
	MaxBatchSize   = 100
	fileVersion    = 1
	defaultDBFile  = "openai-usage.json"
	PricingSource  = "Sub2API / Wei-Shaw LiteLLM-compatible price table https://github.com/Wei-Shaw/sub2api"
	PricingVersion = "sub2api-model-pricing-2026-07-17"
)

var retryDelays = []time.Duration{
	100 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
}

// Store exposes the read-only management operations for OpenAI OAuth JSON usage.
type Store interface {
	Status() StatusResponse
	Accounts() []AccountStats
	Account(authIndex string) (AccountStats, bool)
}

// QuotaSampleRecorder records Codex quota samples without expanding the read-only Store interface.
type QuotaSampleRecorder interface {
	RecordQuotaSample(ctx context.Context, sample QuotaSample) error
}

// StatusResponse is returned by /v0/management/openai-usage/status.
type StatusResponse struct {
	Enabled             bool   `json:"enabled"`
	Path                string `json:"path"`
	QueueLimit          int    `json:"queue_limit"`
	BatchSize           int    `json:"batch_size"`
	DroppedEvents       uint64 `json:"dropped_events"`
	PricingMissingCount int64  `json:"pricing_missing_count"`
	PricingSource       string `json:"pricing_source"`
	PricingVersion      string `json:"pricing_version"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

// AccountsResponse is returned by /v0/management/openai-usage/accounts.
type AccountsResponse struct {
	Accounts            []AccountStats `json:"accounts"`
	Total               int            `json:"total"`
	DroppedEvents       uint64         `json:"dropped_events"`
	PricingMissingCount int64          `json:"pricing_missing_count"`
}

// AccountStats is the fixed public account/file aggregate.
type AccountStats struct {
	AuthIndex                      string  `json:"auth_index"`
	AuthFileName                   string  `json:"auth_file_name"`
	DisplayName                    string  `json:"display_name,omitempty"`
	Provider                       string  `json:"provider,omitempty"`
	AuthType                       string  `json:"auth_type,omitempty"`
	AccountEmail                   string  `json:"account_email"`
	RequestCount                   int64   `json:"request_count"`
	InputTokens                    int64   `json:"input_tokens"`
	CachedInputTokens              int64   `json:"cached_input_tokens"`
	OutputTokens                   int64   `json:"output_tokens"`
	ReasoningTokens                int64   `json:"reasoning_tokens"`
	TotalTokens                    int64   `json:"total_tokens"`
	UsageMissingCount              int64   `json:"usage_missing_count"`
	PricingMissingCount            int64   `json:"pricing_missing_count"`
	EstimatedCostNanoUSD           int64   `json:"estimated_cost_nano_usd"`
	EstimatedCostUSD               string  `json:"estimated_cost_usd"`
	FullQuotaEstimatedNanoUSD      int64   `json:"full_quota_estimated_nano_usd,omitempty"`
	FullQuotaEstimatedUSD          string  `json:"full_quota_estimated_usd,omitempty"`
	QuotaSampleWindowID            string  `json:"quota_sample_window_id,omitempty"`
	QuotaSampleUsedPercent         float64 `json:"quota_sample_used_percent,omitempty"`
	QuotaSampleCostNanoUSD         int64   `json:"quota_sample_cost_nano_usd,omitempty"`
	QuotaSampleUsageMissingCount   int64   `json:"quota_sample_usage_missing_count,omitempty"`
	QuotaSamplePricingMissingCount int64   `json:"quota_sample_pricing_missing_count,omitempty"`
	QuotaSampledAt                 string  `json:"quota_sampled_at,omitempty"`
	QuotaEstimateSamplePercent     float64 `json:"quota_estimate_sample_percent,omitempty"`
	QuotaEstimateSampleCostNanoUSD int64   `json:"quota_estimate_sample_cost_nano_usd,omitempty"`
	QuotaEstimateUpdatedAt         string  `json:"quota_estimate_updated_at,omitempty"`
	LastRequestedAt                string  `json:"last_requested_at,omitempty"`
	lastRequestedAtTimeUTC         time.Time
}

type QuotaSample struct {
	AuthIndex    string
	AuthFileName string
	DisplayName  string
	Provider     string
	AuthType     string
	AccountEmail string
	WindowID     string
	UsedPercent  float64
	SampledAt    time.Time
}

type fileData struct {
	Version        int                     `json:"version"`
	PricingSource  string                  `json:"pricing_source"`
	PricingVersion string                  `json:"pricing_version"`
	UpdatedAt      string                  `json:"updated_at,omitempty"`
	DroppedEvents  uint64                  `json:"dropped_events"`
	Accounts       map[string]AccountStats `json:"accounts"`
}

type Event struct {
	AuthIndex           string
	AuthFileName        string
	DisplayName         string
	Provider            string
	AuthType            string
	AccountEmail        string
	Model               string
	ServiceTier         string
	ResponseServiceTier string
	RequestedAt         time.Time
	UsageReported       bool
	TotalTokensReported bool
	InputTokens         int64
	CachedInputTokens   int64
	CacheCreationTokens int64
	OutputTokens        int64
	ReasoningTokens     int64
	TotalTokens         int64
}

// PersistentUsagePlugin records OpenAI-compatible usage into a JSON file.
type PersistentUsagePlugin struct {
	path   string
	events chan Event
	done   chan struct{}
	once   sync.Once
	cancel context.CancelFunc

	mu            sync.RWMutex
	accounts      map[string]AccountStats
	updatedAt     time.Time
	droppedEvents atomic.Uint64

	saveMu sync.Mutex
	saveFn func(context.Context) error
}

// ResolvePath returns the fixed local data file path relative to the config file directory.
func ResolvePath(configPath string) string {
	baseDir := strings.TrimSpace(filepath.Dir(configPath))
	if configPath == "" || baseDir == "." {
		if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
			baseDir = wd
		}
	}
	return filepath.Join(baseDir, "data", defaultDBFile)
}

// NewPersistentUsagePlugin creates and starts the JSON persistence worker.
func NewPersistentUsagePlugin(path string) *PersistentUsagePlugin {
	path = strings.TrimSpace(path)
	if path == "" {
		path = ResolvePath("")
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	p := &PersistentUsagePlugin{
		path:     path,
		events:   make(chan Event, QueueLimit),
		done:     make(chan struct{}),
		cancel:   cancel,
		accounts: make(map[string]AccountStats),
	}
	p.saveFn = p.saveLocked
	p.load()
	go p.run(workerCtx)
	return p
}

func (p *PersistentUsagePlugin) Status() StatusResponse {
	if p == nil {
		return StatusResponse{Enabled: false}
	}
	p.mu.RLock()
	updatedAt := formatTime(p.updatedAt)
	pricingMissingCount := p.pricingMissingCountLocked()
	p.mu.RUnlock()
	return StatusResponse{
		Enabled:             true,
		Path:                p.path,
		QueueLimit:          QueueLimit,
		BatchSize:           MaxBatchSize,
		DroppedEvents:       p.droppedEvents.Load(),
		PricingMissingCount: pricingMissingCount,
		PricingSource:       PricingSource,
		PricingVersion:      PricingVersion,
		UpdatedAt:           updatedAt,
	}
}

func (p *PersistentUsagePlugin) Accounts() []AccountStats {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	out := make([]AccountStats, 0, len(p.accounts))
	for _, account := range p.accounts {
		out = append(out, accountForOutput(account))
	}
	p.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		left := out[i].lastRequestedAtTimeUTC
		right := out[j].lastRequestedAtTimeUTC
		if !left.Equal(right) {
			return left.After(right)
		}
		return strings.ToLower(out[i].AuthFileName) < strings.ToLower(out[j].AuthFileName)
	})
	return out
}

func (p *PersistentUsagePlugin) Account(authIndex string) (AccountStats, bool) {
	authIndex = strings.TrimSpace(authIndex)
	if p == nil || authIndex == "" {
		return AccountStats{}, false
	}
	p.mu.RLock()
	account, ok := p.accounts[authIndex]
	p.mu.RUnlock()
	if ok {
		account = accountForOutput(account)
	}
	return account, ok
}

func (p *PersistentUsagePlugin) RecordQuotaSample(ctx context.Context, sample QuotaSample) error {
	if p == nil {
		return nil
	}
	sample.AuthIndex = strings.TrimSpace(sample.AuthIndex)
	if sample.AuthIndex == "" {
		return nil
	}
	sample.WindowID = strings.ToLower(strings.TrimSpace(sample.WindowID))
	if sample.WindowID != "weekly" && sample.WindowID != "monthly" {
		return nil
	}
	if !isFinitePercent(sample.UsedPercent) {
		return nil
	}
	if sample.SampledAt.IsZero() {
		sample.SampledAt = time.Now().UTC()
	} else {
		sample.SampledAt = sample.SampledAt.UTC()
	}
	p.mu.Lock()
	account := p.accounts[sample.AuthIndex]
	account.AuthIndex = sample.AuthIndex
	if strings.TrimSpace(sample.Provider) != "" {
		account.Provider = sample.Provider
	}
	if strings.TrimSpace(sample.AuthType) != "" {
		account.AuthType = sample.AuthType
	}
	if strings.TrimSpace(sample.AuthFileName) != "" {
		account.AuthFileName = filepath.Base(strings.TrimSpace(sample.AuthFileName))
	}
	if strings.TrimSpace(sample.DisplayName) != "" {
		account.DisplayName = strings.TrimSpace(sample.DisplayName)
	} else if account.DisplayName == "" && account.AuthFileName != "" {
		account.DisplayName = account.AuthFileName
	}
	if strings.TrimSpace(sample.AccountEmail) != "" {
		account.AccountEmail = strings.TrimSpace(sample.AccountEmail)
	}

	previousWindow := strings.TrimSpace(account.QuotaSampleWindowID)
	previousPercent := account.QuotaSampleUsedPercent
	previousCost := account.QuotaSampleCostNanoUSD
	previousUsageMissing := account.QuotaSampleUsageMissingCount
	previousPricingMissing := account.QuotaSamplePricingMissingCount
	currentCost := account.EstimatedCostNanoUSD
	currentUsageMissing := account.UsageMissingCount
	currentPricingMissing := account.PricingMissingCount

	if previousWindow == sample.WindowID &&
		sample.UsedPercent > previousPercent &&
		currentCost > previousCost &&
		sample.UsedPercent-previousPercent >= 1.0 &&
		currentUsageMissing == previousUsageMissing &&
		currentPricingMissing == previousPricingMissing {
		percentDelta := sample.UsedPercent - previousPercent
		costDelta := currentCost - previousCost
		account.FullQuotaEstimatedNanoUSD = int64(math.Round(float64(costDelta) / (percentDelta / 100)))
		account.FullQuotaEstimatedUSD = FormatUSD(account.FullQuotaEstimatedNanoUSD)
		account.QuotaEstimateSamplePercent = percentDelta
		account.QuotaEstimateSampleCostNanoUSD = costDelta
		account.QuotaEstimateUpdatedAt = sample.SampledAt.Format(time.RFC3339)
	}

	account.QuotaSampleWindowID = sample.WindowID
	account.QuotaSampleUsedPercent = sample.UsedPercent
	account.QuotaSampleCostNanoUSD = currentCost
	account.QuotaSampleUsageMissingCount = currentUsageMissing
	account.QuotaSamplePricingMissingCount = currentPricingMissing
	account.QuotaSampledAt = sample.SampledAt.Format(time.RFC3339)
	account = accountForOutput(account)
	p.accounts[sample.AuthIndex] = account
	if p.updatedAt.IsZero() || sample.SampledAt.After(p.updatedAt) {
		p.updatedAt = sample.SampledAt
	}
	p.mu.Unlock()
	return p.save(ctx)
}

func (p *PersistentUsagePlugin) HandleUsage(_ context.Context, record coreusage.Record) {
	event, ok := EventFromRecord(record)
	if !ok || p == nil {
		return
	}
	select {
	case p.events <- event:
	default:
		dropped := p.droppedEvents.Add(1)
		log.WithField("dropped_events", dropped).Warn("openai usage: queue full, dropped usage event")
	}
}

func (p *PersistentUsagePlugin) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.once.Do(func() {
		close(p.events)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		if p.cancel != nil {
			p.cancel()
		}
		<-p.done
		return ctx.Err()
	}
}

func (p *PersistentUsagePlugin) run(ctx context.Context) {
	defer close(p.done)
	var batch []Event
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		if len(batch) == 0 {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-p.events:
				if !ok {
					return
				}
				batch = append(batch, event)
				timer.Reset(500 * time.Millisecond)
			}
		}
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case event, ok := <-p.events:
			if !ok {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				p.flushWithRetry(ctx, batch)
				return
			}
			batch = append(batch, event)
			if len(batch) >= MaxBatchSize {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				p.flushWithRetry(ctx, batch)
				batch = nil
			}
		case <-timer.C:
			p.flushWithRetry(ctx, batch)
			batch = nil
		}
	}
}

func (p *PersistentUsagePlugin) flushWithRetry(ctx context.Context, batch []Event) {
	if p == nil || len(batch) == 0 {
		return
	}
	p.apply(batch)
	attempt := 0
	for {
		if err := p.save(ctx); err != nil {
			delay := retryDelay(attempt)
			attempt++
			log.WithError(err).Warnf("openai usage: persist failed, retrying in %s", delay)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				continue
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		return
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt]
}

func (p *PersistentUsagePlugin) apply(batch []Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().UTC()
	for _, event := range batch {
		account := p.accounts[event.AuthIndex]
		account.AuthIndex = event.AuthIndex
		if strings.TrimSpace(event.Provider) != "" {
			account.Provider = event.Provider
		}
		if strings.TrimSpace(event.AuthType) != "" {
			account.AuthType = event.AuthType
		}
		if strings.TrimSpace(event.AuthFileName) != "" {
			account.AuthFileName = event.AuthFileName
		}
		if strings.TrimSpace(event.DisplayName) != "" {
			account.DisplayName = event.DisplayName
		}
		if strings.TrimSpace(event.AccountEmail) != "" {
			account.AccountEmail = event.AccountEmail
		}
		account.RequestCount++
		requestedAt := event.RequestedAt.UTC()
		if requestedAt.IsZero() {
			requestedAt = now
		}
		if account.lastRequestedAtTimeUTC.IsZero() || requestedAt.After(account.lastRequestedAtTimeUTC) {
			account.lastRequestedAtTimeUTC = requestedAt
			account.LastRequestedAt = requestedAt.Format(time.RFC3339)
		}
		if !event.UsageReported {
			account.UsageMissingCount++
			p.accounts[event.AuthIndex] = accountForOutput(account)
			continue
		}
		account.InputTokens += event.InputTokens
		account.CachedInputTokens += event.CachedInputTokens
		account.OutputTokens += event.OutputTokens
		account.ReasoningTokens += event.ReasoningTokens
		account.TotalTokens += normalizedTotal(event)
		if cost, ok := Price(event); ok {
			account.EstimatedCostNanoUSD += cost
		} else {
			account.PricingMissingCount++
		}
		p.accounts[event.AuthIndex] = accountForOutput(account)
	}
	p.updatedAt = now
}

func normalizedTotal(event Event) int64 {
	if event.TotalTokensReported {
		return event.TotalTokens
	}
	return event.InputTokens + event.OutputTokens
}

func (p *PersistentUsagePlugin) save(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	if p.saveFn != nil {
		return p.saveFn(ctx)
	}
	return p.saveLocked(ctx)
}

func (p *PersistentUsagePlugin) saveLocked(ctx context.Context) error {
	if p == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	p.mu.RLock()
	data := fileData{
		Version:        fileVersion,
		PricingSource:  PricingSource,
		PricingVersion: PricingVersion,
		UpdatedAt:      formatTime(p.updatedAt),
		DroppedEvents:  p.droppedEvents.Load(),
		Accounts:       make(map[string]AccountStats, len(p.accounts)),
	}
	for key, account := range p.accounts {
		data.Accounts[key] = accountForOutput(account)
	}
	p.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return fmt.Errorf("create openai usage data dir: %w", err)
	}
	payload, errMarshal := json.MarshalIndent(data, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal openai usage data: %w", errMarshal)
	}
	tmp := p.path + ".tmp"
	if errWrite := os.WriteFile(tmp, payload, 0o644); errWrite != nil {
		return fmt.Errorf("write openai usage temp file: %w", errWrite)
	}
	if errRename := os.Rename(tmp, p.path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace openai usage file: %w", errRename)
	}
	return nil
}

func (p *PersistentUsagePlugin) load() {
	if p == nil {
		return
	}
	data, errRead := os.ReadFile(p.path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.WithError(errRead).Warn("openai usage: failed to read usage file")
		}
		return
	}
	var parsed fileData
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("openai usage: failed to parse usage file")
		return
	}
	p.mu.Lock()
	p.accounts = make(map[string]AccountStats, len(parsed.Accounts))
	for key, account := range parsed.Accounts {
		account.AuthIndex = strings.TrimSpace(account.AuthIndex)
		if account.AuthIndex == "" {
			account.AuthIndex = strings.TrimSpace(key)
		}
		if ts, errParse := time.Parse(time.RFC3339, account.LastRequestedAt); errParse == nil {
			account.lastRequestedAtTimeUTC = ts.UTC()
		}
		account = accountForOutput(account)
		if account.AuthIndex != "" {
			p.accounts[account.AuthIndex] = account
		}
	}
	if ts, errParse := time.Parse(time.RFC3339, parsed.UpdatedAt); errParse == nil {
		p.updatedAt = ts.UTC()
	}
	p.mu.Unlock()
	p.droppedEvents.Store(parsed.DroppedEvents)
}

func (p *PersistentUsagePlugin) pricingMissingCountLocked() int64 {
	if p == nil {
		return 0
	}
	var total int64
	for _, account := range p.accounts {
		total += account.PricingMissingCount
	}
	return total
}

func accountForOutput(account AccountStats) AccountStats {
	account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
	if account.FullQuotaEstimatedNanoUSD > 0 {
		account.FullQuotaEstimatedUSD = FormatUSD(account.FullQuotaEstimatedNanoUSD)
	} else {
		account.FullQuotaEstimatedUSD = ""
	}
	return account
}

func isFinitePercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

// EventFromRecord applies the fixed inclusion rule for this feature.
func EventFromRecord(record coreusage.Record) (Event, bool) {
	provider := normalizeUsageProvider(record.Provider)
	if !isOpenAIUsageProvider(provider) {
		return Event{}, false
	}
	authType := normalizeUsageAuthType(record.AuthType)
	if authType != "oauth" && authType != "apikey" {
		return Event{}, false
	}
	authIndex := strings.TrimSpace(record.AuthIndex)
	if authIndex == "" {
		return Event{}, false
	}
	fileName := strings.TrimSpace(record.AuthFileName)
	if fileName != "" {
		fileName = filepath.Base(fileName)
	}
	displayName := fileName
	if authType == "oauth" {
		if fileName == "" || !strings.HasSuffix(strings.ToLower(fileName), ".json") {
			return Event{}, false
		}
	} else {
		displayName = apiKeyDisplayName(authIndex)
		fileName = displayName
	}
	detail := record.Detail
	cacheRead := detail.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = detail.CachedTokens
	}
	return Event{
		AuthIndex:           authIndex,
		AuthFileName:        fileName,
		DisplayName:         displayName,
		Provider:            provider,
		AuthType:            authType,
		AccountEmail:        strings.TrimSpace(record.AccountEmail),
		Model:               strings.TrimSpace(record.Model),
		ServiceTier:         strings.TrimSpace(record.ServiceTier),
		ResponseServiceTier: strings.TrimSpace(record.ResponseServiceTier),
		RequestedAt:         record.RequestedAt,
		UsageReported:       record.UsageReported || detail.UsageReported,
		TotalTokensReported: record.TotalTokensReported || detail.TotalTokensReported,
		InputTokens:         detail.InputTokens,
		CachedInputTokens:   cacheRead,
		CacheCreationTokens: detail.CacheCreationTokens,
		OutputTokens:        detail.OutputTokens,
		ReasoningTokens:     detail.ReasoningTokens,
		TotalTokens:         detail.TotalTokens,
	}, true
}

// FormatUSD formats nano-USD as a decimal USD string.
func FormatUSD(nano int64) string {
	if nano == 0 {
		return "0.000000000"
	}
	sign := ""
	if nano < 0 {
		sign = "-"
		nano = -nano
	}
	whole := nano / 1_000_000_000
	frac := nano % 1_000_000_000
	return fmt.Sprintf("%s%d.%09d", sign, whole, frac)
}

func formatTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}
