package usagepricing

import (
	"strings"
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
)

const (
	Currency  = "USD"
	SourceURL = "https://api-docs.deepseek.com/quick_start/pricing"
)

var ProDiscountEndsAt = time.Date(2026, 5, 31, 15, 59, 0, 0, time.UTC)

type ModelPricing struct {
	InputCacheHitPerMillion  float64 `json:"input_cache_hit_per_1m"`
	InputCacheMissPerMillion float64 `json:"input_cache_miss_per_1m"`
	OutputPerMillion         float64 `json:"output_per_1m"`
}

func CalculateUSD(byModel map[string]chathistory.TokenUsageTotals, now time.Time) float64 {
	var total float64
	for model, usage := range byModel {
		price := PriceForModel(model, now)
		hit := float64(usage.CacheHitInputTokens) * price.InputCacheHitPerMillion
		miss := float64(usage.CacheMissInputTokens) * price.InputCacheMissPerMillion
		output := float64(usage.OutputTokens) * price.OutputPerMillion
		total += (hit + miss + output) / 1_000_000
	}
	return total
}

func PriceForModel(model string, now time.Time) ModelPricing {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(normalized, "pro") {
		if now.UTC().Before(ProDiscountEndsAt) {
			return ModelPricing{
				InputCacheHitPerMillion:  0.003625,
				InputCacheMissPerMillion: 0.435,
				OutputPerMillion:         0.87,
			}
		}
		return ModelPricing{
			InputCacheHitPerMillion:  0.0145,
			InputCacheMissPerMillion: 1.74,
			OutputPerMillion:         3.48,
		}
	}
	return ModelPricing{
		InputCacheHitPerMillion:  0.0028,
		InputCacheMissPerMillion: 0.14,
		OutputPerMillion:         0.28,
	}
}
