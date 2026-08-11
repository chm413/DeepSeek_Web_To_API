package usagepricing

import (
	"math"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/chathistory"
)

func TestCalculateUSDUsesCacheSplitAndModelPricing(t *testing.T) {
	usage := map[string]chathistory.TokenUsageTotals{
		"deepseek-v4-flash": {
			CacheHitInputTokens:  100,
			CacheMissInputTokens: 200,
			OutputTokens:         300,
		},
	}
	want := (100*0.0028 + 200*0.14 + 300*0.28) / 1_000_000
	got := CalculateUSD(usage, time.Now())
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("unexpected cost: got %.12f want %.12f", got, want)
	}
}
