package openaiusage

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"sync"
)

//go:embed model_prices_and_context_window.json
var sub2APIPriceData []byte

var (
	priceTableOnce sync.Once
	priceTable     map[string]modelPrice
	priceTableErr  error

	dateSuffixPattern        = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)
	compactDateSuffixPattern = regexp.MustCompile(`-\d{8}$`)
	aboveCostFieldPattern    = regexp.MustCompile(`_above_(\d+)k_tokens$`)
)

type modelPrice struct {
	Name                 string
	Provider             string
	Costs                map[string]int64
	LongThreshold        int64
	LongInputMultiplier  *big.Rat
	LongOutputMultiplier *big.Rat
}

// Price calculates equivalent API estimated cost in nano-USD.
func Price(event Event) (int64, bool) {
	if !event.UsageReported {
		return 0, false
	}
	model, ok := lookupModelPrice(event.Model)
	if !ok {
		return 0, false
	}
	tierName := finalTier(event)
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

	cost := int64(0)
	if regularInput > 0 {
		price, okPrice := model.priceFor("input_cost_per_token", tierName, event.InputTokens)
		if !okPrice {
			return 0, false
		}
		cost += regularInput * price
	}
	if cacheRead > 0 {
		price, okPrice := model.priceFor("cache_read_input_token_cost", tierName, event.InputTokens)
		if !okPrice {
			price, okPrice = model.priceFor("input_cost_per_token", tierName, event.InputTokens)
			if !okPrice {
				return 0, false
			}
		}
		cost += cacheRead * price
	}
	if cacheCreation > 0 {
		price, okPrice := model.priceFor("cache_creation_input_token_cost", tierName, event.InputTokens)
		if !okPrice {
			price, okPrice = model.priceFor("input_cost_per_token", tierName, event.InputTokens)
			if !okPrice {
				return 0, false
			}
		}
		cost += cacheCreation * price
	}
	if event.OutputTokens > 0 {
		price, okPrice := model.priceFor("output_cost_per_token", tierName, event.InputTokens)
		if !okPrice {
			return 0, false
		}
		cost += event.OutputTokens * price
	}
	return cost, true
}

func lookupModelPrice(model string) (modelPrice, bool) {
	table := loadPriceTable()
	for _, candidate := range modelLookupCandidates(model) {
		if price, ok := table[candidate]; ok {
			return price, true
		}
	}
	return modelPrice{}, false
}

func loadPriceTable() map[string]modelPrice {
	priceTableOnce.Do(func() {
		priceTable, priceTableErr = parsePriceTable(sub2APIPriceData)
	})
	if priceTable == nil || priceTableErr != nil {
		return map[string]modelPrice{}
	}
	return priceTable
}

func parsePriceTable(data []byte) (map[string]modelPrice, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	raw := map[string]map[string]json.RawMessage{}
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	out := make(map[string]modelPrice, len(raw))
	for name, fields := range raw {
		provider := rawString(fields["litellm_provider"])
		if !strings.EqualFold(provider, "openai") {
			continue
		}
		price := modelPrice{
			Name:     strings.ToLower(strings.TrimSpace(name)),
			Provider: strings.ToLower(strings.TrimSpace(provider)),
			Costs:    map[string]int64{},
		}
		for field, value := range fields {
			if !isCostField(field) {
				continue
			}
			if nano, ok := rawDecimalUSDToNano(value); ok {
				price.Costs[field] = nano
			}
			if matches := aboveCostFieldPattern.FindStringSubmatch(field); len(matches) == 2 {
				if threshold, ok := parseInt64(matches[1]); ok && threshold > 0 {
					threshold *= 1000
					if price.LongThreshold == 0 || threshold < price.LongThreshold {
						price.LongThreshold = threshold
					}
				}
			}
		}
		if threshold, ok := rawInt64(fields["long_context_input_token_threshold"]); ok && threshold > 0 {
			price.LongThreshold = threshold
		}
		price.LongInputMultiplier = rawDecimalRat(fields["long_context_input_cost_multiplier"])
		price.LongOutputMultiplier = rawDecimalRat(fields["long_context_output_cost_multiplier"])
		if price.Name != "" {
			out[price.Name] = price
		}
	}
	return out, nil
}

func isCostField(field string) bool {
	switch {
	case strings.HasPrefix(field, "input_cost_per_token"):
		return true
	case strings.HasPrefix(field, "output_cost_per_token"):
		return true
	case strings.HasPrefix(field, "cache_read_input_token_cost"):
		return true
	case strings.HasPrefix(field, "cache_creation_input_token_cost"):
		return true
	default:
		return false
	}
}

func (m modelPrice) priceFor(baseField string, tier string, inputTokens int64) (int64, bool) {
	price, ok := m.priceForTier(baseField, tier)
	if !ok {
		return 0, false
	}
	if m.longThresholdApplies(inputTokens) {
		if tier == "default" {
			if above, okAbove := m.priceAboveThreshold(baseField); okAbove {
				return above, true
			}
		}
		if multiplier := m.longMultiplier(baseField); multiplier != nil {
			return multiplyNano(price, multiplier), true
		}
	}
	return price, true
}

func (m modelPrice) priceForTier(baseField string, tier string) (int64, bool) {
	switch tier {
	case "priority":
		if price, ok := m.Costs[baseField+"_priority"]; ok {
			return price, true
		}
		base, ok := m.Costs[baseField]
		if !ok {
			return 0, false
		}
		return multiplyNano(base, big.NewRat(2, 1)), true
	case "flex":
		if price, ok := m.Costs[baseField+"_flex"]; ok {
			return price, true
		}
		base, ok := m.Costs[baseField]
		if !ok {
			return 0, false
		}
		return multiplyNano(base, big.NewRat(1, 2)), true
	case "batch":
		if price, ok := m.Costs[baseField+"_batches"]; ok {
			return price, true
		}
		if price, ok := m.Costs[baseField+"_batch"]; ok {
			return price, true
		}
	}
	price, ok := m.Costs[baseField]
	return price, ok
}

func (m modelPrice) longThresholdApplies(inputTokens int64) bool {
	return m.LongThreshold > 0 && inputTokens >= m.LongThreshold
}

func (m modelPrice) priceAboveThreshold(baseField string) (int64, bool) {
	if m.LongThreshold <= 0 {
		return 0, false
	}
	field := baseField + "_above_" + formatThresholdK(m.LongThreshold) + "k_tokens"
	price, ok := m.Costs[field]
	return price, ok
}

func (m modelPrice) longMultiplier(baseField string) *big.Rat {
	if strings.HasPrefix(baseField, "output_cost_per_token") {
		return m.LongOutputMultiplier
	}
	return m.LongInputMultiplier
}

func finalTier(event Event) string {
	if strings.TrimSpace(event.ResponseServiceTier) != "" {
		return normalizeTier(event.ResponseServiceTier)
	}
	return normalizeTier(event.ServiceTier)
}

func normalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "", "auto", "standard", "default", "scale":
		return "default"
	case "fast", "priority":
		return "priority"
	case "batch", "batches":
		return "batch"
	case "flex":
		return "flex"
	default:
		return "default"
	}
}

// CanonicalModel returns the first normalized lookup candidate for diagnostics.
func CanonicalModel(model string) string {
	candidates := modelLookupCandidates(model)
	if len(candidates) == 0 {
		return ""
	}
	table := loadPriceTable()
	for _, candidate := range candidates {
		if _, ok := table[candidate]; ok {
			return candidate
		}
	}
	return candidates[0]
}

func modelLookupCandidates(model string) []string {
	original := strings.ToLower(strings.TrimSpace(model))
	if original == "" {
		return nil
	}
	var candidates []string
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		candidates = append(candidates, value)
	}
	for _, seed := range []string{
		original,
		strings.TrimPrefix(original, "models/"),
		lastModelSegment(original),
	} {
		current := seed
		for i := 0; i < 5; i++ {
			add(current)
			next := canonicalModelStep(current)
			if next == current {
				break
			}
			current = next
		}
		if mapped := openAIExplicitModelAlias(current); mapped != current {
			add(mapped)
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func canonicalModelStep(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/models/"); idx >= 0 && idx+len("/models/") < len(model) {
		model = model[idx+len("/models/"):]
	}
	if slash := strings.LastIndex(model, "/"); slash >= 0 && slash < len(model)-1 {
		model = strings.TrimSpace(model[slash+1:])
	}
	if idx := strings.Index(model, "("); idx > 0 && strings.HasSuffix(model, ")") {
		model = strings.TrimSpace(model[:idx])
	}
	model = compactDateSuffixPattern.ReplaceAllString(model, "")
	model = dateSuffixPattern.ReplaceAllString(model, "")
	return model
}

func openAIExplicitModelAlias(model string) string {
	model = canonicalModelStep(model)
	if model == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	if strings.HasPrefix(model, "gpt-5.6-") {
		switch strings.TrimPrefix(model, "gpt-5.6-") {
		case "sol", "terra", "luna":
			return model
		case "max", "codex", "codex-max", "codex-mini":
			return "gpt-5.6-sol"
		}
	}
	return model
}

func lastModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return model[idx+1:]
	}
	return model
}

func normalizeUsageProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func isOpenAIUsageProvider(provider string) bool {
	return provider == "codex" ||
		provider == "openai" ||
		provider == "openai-compatibility" ||
		strings.HasPrefix(provider, "openai-compatible-")
}

func normalizeUsageAuthType(authType string) string {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "oauth", "oauth2":
		return "oauth"
	case "apikey", "api_key", "api-key":
		return "apikey"
	default:
		return ""
	}
}

func apiKeyDisplayName(authIndex string) string {
	authIndex = strings.TrimSpace(authIndex)
	if len(authIndex) > 8 {
		authIndex = authIndex[:8]
	}
	if authIndex == "" {
		return "api-key"
	}
	return "api-key:" + authIndex
}

func rawString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(string(raw))
}

func rawInt64(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return parseInt64(number.String())
	}
	return parseInt64(rawString(raw))
}

func parseInt64(value string) (int64, bool) {
	rat := rawDecimalRatFromString(value)
	if rat == nil {
		return 0, false
	}
	return roundRat(rat), true
}

func rawDecimalUSDToNano(raw json.RawMessage) (int64, bool) {
	rat := rawDecimalRat(raw)
	if rat == nil {
		return 0, false
	}
	rat.Mul(rat, big.NewRat(1_000_000_000, 1))
	return roundRat(rat), true
}

func rawDecimalRat(raw json.RawMessage) *big.Rat {
	return rawDecimalRatFromString(rawString(raw))
}

func rawDecimalRatFromString(value string) *big.Rat {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	floatValue, _, err := big.ParseFloat(value, 10, 256, big.ToNearestEven)
	if err != nil {
		return nil
	}
	rat, _ := floatValue.Rat(nil)
	return rat
}

func multiplyNano(value int64, multiplier *big.Rat) int64 {
	if multiplier == nil {
		return value
	}
	rat := big.NewRat(value, 1)
	rat.Mul(rat, multiplier)
	return roundRat(rat)
}

func roundRat(rat *big.Rat) int64 {
	if rat == nil {
		return 0
	}
	num := new(big.Int).Set(rat.Num())
	den := new(big.Int).Set(rat.Denom())
	if num.Sign() >= 0 {
		num.Add(num, new(big.Int).Div(den, big.NewInt(2)))
	} else {
		num.Sub(num, new(big.Int).Div(den, big.NewInt(2)))
	}
	return new(big.Int).Quo(num, den).Int64()
}

func formatThresholdK(threshold int64) string {
	if threshold%1000 == 0 {
		return strconvFormatInt(threshold / 1000)
	}
	return strconvFormatInt(threshold)
}

func strconvFormatInt(value int64) string {
	return new(big.Int).SetInt64(value).String()
}
