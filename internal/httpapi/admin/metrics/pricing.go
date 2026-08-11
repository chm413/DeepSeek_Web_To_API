package metrics

import (
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
	"DeepSeek_Web_To_API/internal/usagepricing"
)

const (
	pricingSourceURL = usagepricing.SourceURL
	pricingCurrency  = usagepricing.Currency
)

var proDiscountEndsAt = usagepricing.ProDiscountEndsAt

type modelPricing = usagepricing.ModelPricing

type costBreakdown struct {
	Currency       string                  `json:"currency"`
	WindowUSD      float64                 `json:"window_usd"`
	TotalUSD       float64                 `json:"total_usd"`
	PricingSource  string                  `json:"pricing_source"`
	PricingNote    string                  `json:"pricing_note"`
	PricingByModel map[string]modelPricing `json:"pricing_by_model"`
	DiscountEndsAt string                  `json:"discount_ends_at,omitempty"`
}

func buildCostBreakdown(stats chathistory.TokenUsageStats, now time.Time) costBreakdown {
	prices := map[string]modelPricing{}
	for model := range stats.TotalByModel {
		prices[model] = priceForModel(model, now)
	}
	for model := range stats.WindowByModel {
		prices[model] = priceForModel(model, now)
	}
	if len(prices) == 0 {
		prices["deepseek-v4-flash"] = priceForModel("deepseek-v4-flash", now)
	}

	return costBreakdown{
		Currency:       pricingCurrency,
		WindowUSD:      calculateCostUSD(stats.WindowByModel, now),
		TotalUSD:       calculateCostUSD(stats.TotalByModel, now),
		PricingSource:  pricingSourceURL,
		PricingNote:    "Estimated from DeepSeek official per-1M-token pricing; input tokens without cache split are billed as cache miss.",
		PricingByModel: prices,
		DiscountEndsAt: proDiscountEndsAt.Format(time.RFC3339),
	}
}

func calculateCostUSD(byModel map[string]chathistory.TokenUsageTotals, now time.Time) float64 {
	return usagepricing.CalculateUSD(byModel, now)
}

func priceForModel(model string, now time.Time) modelPricing {
	return usagepricing.PriceForModel(model, now)
}
