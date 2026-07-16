package openaiusage

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
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
	pricingSource  = "OpenAI API pricing page https://developers.openai.com/api/docs/pricing checked 2026-07-14"
	PricingVersion = "openai-official-pricing-2026-07-14"
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
	AuthIndex              string `json:"auth_index"`
	AuthFileName           string `json:"auth_file_name"`
	AccountEmail           string `json:"account_email"`
	RequestCount           int64  `json:"request_count"`
	InputTokens            int64  `json:"input_tokens"`
	CachedInputTokens      int64  `json:"cached_input_tokens"`
	OutputTokens           int64  `json:"output_tokens"`
	ReasoningTokens        int64  `json:"reasoning_tokens"`
	TotalTokens            int64  `json:"total_tokens"`
	UsageMissingCount      int64  `json:"usage_missing_count"`
	PricingMissingCount    int64  `json:"pricing_missing_count"`
	EstimatedCostNanoUSD   int64  `json:"estimated_cost_nano_usd"`
	EstimatedCostUSD       string `json:"estimated_cost_usd"`
	LastRequestedAt        string `json:"last_requested_at,omitempty"`
	lastRequestedAtTimeUTC time.Time
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

// PersistentUsagePlugin records only OpenAI/Codex OAuth JSON usage into a JSON file.
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
		PricingSource:       pricingSource,
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
		account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
		out = append(out, account)
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
		account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
	}
	return account, ok
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
		if strings.TrimSpace(event.AuthFileName) != "" {
			account.AuthFileName = event.AuthFileName
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
			p.accounts[event.AuthIndex] = account
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
		account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
		p.accounts[event.AuthIndex] = account
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
		PricingSource:  pricingSource,
		PricingVersion: PricingVersion,
		UpdatedAt:      formatTime(p.updatedAt),
		DroppedEvents:  p.droppedEvents.Load(),
		Accounts:       make(map[string]AccountStats, len(p.accounts)),
	}
	for key, account := range p.accounts {
		account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
		data.Accounts[key] = account
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
		account.EstimatedCostUSD = FormatUSD(account.EstimatedCostNanoUSD)
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

// EventFromRecord applies the fixed inclusion rule for this feature.
func EventFromRecord(record coreusage.Record) (Event, bool) {
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	if provider != "codex" && provider != "openai" {
		return Event{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(record.AuthType), "oauth") {
		return Event{}, false
	}
	authIndex := strings.TrimSpace(record.AuthIndex)
	if authIndex == "" {
		return Event{}, false
	}
	fileName := strings.TrimSpace(record.AuthFileName)
	if fileName == "" || !strings.HasSuffix(strings.ToLower(fileName), ".json") {
		return Event{}, false
	}
	fileName = filepath.Base(fileName)
	detail := record.Detail
	cacheRead := detail.CacheReadTokens
	if cacheRead == 0 {
		cacheRead = detail.CachedTokens
	}
	return Event{
		AuthIndex:           authIndex,
		AuthFileName:        fileName,
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

type modelPrice struct {
	Short modelTierPrices
	Long  *modelTierPrices
}

type modelTierPrices struct {
	ThresholdTokens int64
	Tiers           map[string]tierPrice
}

type tierPrice struct {
	InputNanoUSDPerToken      int64
	CachedNanoUSDPerToken     int64
	OutputNanoUSDPerToken     int64
	CacheWriteNanoUSDPerToken *int64
}

var priceTable = map[string]modelPrice{
	"gpt-5.5": {
		Short: modelTierPrices{
			Tiers: map[string]tierPrice{
				"default":  pricePerMillion("5.000", "0.500", "30.000"),
				"priority": pricePerMillion("12.500", "1.250", "75.000"),
				"batch":    pricePerMillion("2.500", "0.250", "15.000"),
				"flex":     pricePerMillion("2.500", "0.250", "15.000"),
			},
		},
		Long: &modelTierPrices{
			ThresholdTokens: 272000,
			Tiers: map[string]tierPrice{
				"default": pricePerMillion("10.000", "1.000", "45.000"),
				"batch":   pricePerMillion("5.000", "0.500", "22.500"),
				"flex":    pricePerMillion("5.000", "0.500", "22.500"),
			},
		},
	},
}

// Price calculates equivalent API estimated cost in nano-USD.
func Price(event Event) (int64, bool) {
	if !event.UsageReported {
		return 0, false
	}
	canonical := CanonicalModel(event.Model)
	model, ok := priceTable[canonical]
	if !ok {
		return 0, false
	}
	tierName := finalTier(event)
	prices := model.Short
	if model.Long != nil && event.InputTokens >= model.Long.ThresholdTokens {
		if _, okTier := model.Long.Tiers[tierName]; !okTier {
			return 0, false
		}
		prices = *model.Long
	}
	price, ok := prices.Tiers[tierName]
	if !ok {
		return 0, false
	}
	cacheRead := event.CachedInputTokens
	if cacheRead < 0 {
		cacheRead = 0
	}
	cacheCreation := event.CacheCreationTokens
	if cacheCreation < 0 {
		cacheCreation = 0
	}
	regularInput := event.InputTokens - cacheRead - cacheCreation
	if regularInput < 0 {
		regularInput = 0
	}
	cost := regularInput*price.InputNanoUSDPerToken +
		cacheRead*price.CachedNanoUSDPerToken +
		event.OutputTokens*price.OutputNanoUSDPerToken
	if cacheCreation > 0 {
		if price.CacheWriteNanoUSDPerToken != nil {
			cost += cacheCreation * *price.CacheWriteNanoUSDPerToken
		} else {
			cost += cacheCreation * price.InputNanoUSDPerToken
		}
	}
	return cost, true
}

func finalTier(event Event) string {
	if strings.TrimSpace(event.ResponseServiceTier) != "" {
		return normalizeTier(event.ResponseServiceTier)
	}
	return normalizeTier(event.ServiceTier)
}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "auto", "standard", "default":
		return "default"
	case "priority":
		return "priority"
	case "batch":
		return "batch"
	case "flex":
		return "flex"
	default:
		return "default"
	}
}

var dateSuffixPattern = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// CanonicalModel normalizes only exact supported OpenAI model naming variants.
func CanonicalModel(model string) string {
	current := strings.ToLower(strings.TrimSpace(model))
	for i := 0; i < 4; i++ {
		next := canonicalModelStep(current)
		if next == current {
			break
		}
		current = next
	}
	return current
}

func canonicalModelStep(model string) string {
	model = strings.TrimSpace(model)
	if slash := strings.LastIndex(model, "/"); slash >= 0 && slash < len(model)-1 {
		model = strings.TrimSpace(model[slash+1:])
	}
	if idx := strings.Index(model, "("); idx > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:idx])
	}
	model = dateSuffixPattern.ReplaceAllString(model, "")
	return model
}

func pricePerMillion(input, cached, output string) tierPrice {
	return tierPrice{
		InputNanoUSDPerToken:  decimalUSDPerMillionToNano(input),
		CachedNanoUSDPerToken: decimalUSDPerMillionToNano(cached),
		OutputNanoUSDPerToken: decimalUSDPerMillionToNano(output),
	}
}

func decimalUSDPerMillionToNano(value string) int64 {
	rat, ok := new(big.Rat).SetString(strings.TrimSpace(value))
	if !ok {
		return 0
	}
	rat.Mul(rat, big.NewRat(1_000_000_000, 1_000_000))
	num := new(big.Int).Set(rat.Num())
	den := new(big.Int).Set(rat.Denom())
	num.Add(num, new(big.Int).Div(den, big.NewInt(2)))
	return new(big.Int).Quo(num, den).Int64()
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
