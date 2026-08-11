package client

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
)

func TestParseModelInputLimitsFindsTieredSettings(t *testing.T) {
	got, err := parseModelInputLimits(map[string]any{
		"code": 0,
		"data": map[string]any{
			"biz_code": 0,
			"biz_data": map[string]any{
				"default": map[string]any{"input_character_limit": float64(2621440)},
				"expert":  map[string]any{"input_character_limit": float64(163840)},
			},
		},
	})
	if err != nil {
		t.Fatalf("parse model settings: %v", err)
	}
	want := config.ModelInputLimits{Default: 2621440, Expert: 163840}
	if got != want {
		t.Fatalf("limits=%+v, want %+v", got, want)
	}
}

func TestGetModelInputLimitsUsesAuthenticatedSettingsEndpointAndCache(t *testing.T) {
	calls := 0
	doer := encodedBodyDoerFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != http.MethodGet || req.URL.Scheme+"://"+req.URL.Host+req.URL.Path != strings.Split(dsprotocol.DeepSeekClientSettingsURL, "?")[0] {
			t.Fatalf("unexpected settings request: %s %s", req.Method, req.URL)
		}
		query, _ := url.ParseQuery(req.URL.RawQuery)
		if query.Get("scope") != "model" || query.Get("did") != modelSettingsDID(&auth.RequestAuth{DeepSeekToken: "upstream-token", AccountID: "account-1"}) {
			t.Fatalf("unexpected settings query: %s", req.URL.RawQuery)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer upstream-token" {
			t.Fatalf("authorization=%q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"code":0,
				"data":{"biz_code":0,"biz_data":{
					"default":{"input_character_limit":2621440},
					"expert":{"input_character_limit":163840}
				}}
			}`)),
			Request: req,
		}, nil
	})
	c := NewClient(nil, nil)
	c.regular = doer
	a := &auth.RequestAuth{DeepSeekToken: "upstream-token", AccountID: "account-1"}

	for i := 0; i < 2; i++ {
		limits, err := c.GetModelInputLimits(context.Background(), a)
		if err != nil {
			t.Fatalf("get model limits: %v", err)
		}
		if limits.Default != 2621440 || limits.Expert != 163840 {
			t.Fatalf("limits=%+v", limits)
		}
	}
	if calls != 1 {
		t.Fatalf("settings endpoint calls=%d, want 1 cache miss", calls)
	}
}

func TestModelSettingsDIDIsStableUUIDShape(t *testing.T) {
	a := &auth.RequestAuth{DeepSeekToken: "secret-token", AccountID: "account-1"}
	first := modelSettingsDID(a)
	second := modelSettingsDID(a)
	if first != second || len(first) != 36 || strings.Count(first, "-") != 4 {
		t.Fatalf("unstable or malformed settings did: %q / %q", first, second)
	}
	if first == modelSettingsDID(&auth.RequestAuth{DeepSeekToken: "other", AccountID: "account-2"}) {
		t.Fatal("different accounts must not share a settings did")
	}
}

func TestParseModelInputLimitsUsesModelNames(t *testing.T) {
	got, err := parseModelInputLimits(map[string]any{
		"models": []any{
			map[string]any{"model": "deepseek-v4-flash", "input_character_limit": 2621440},
			map[string]any{"model": "deepseek-v4-pro-search", "input_character_limit": 163840},
		},
	})
	if err != nil {
		t.Fatalf("parse model settings: %v", err)
	}
	if got.Default != 2621440 || got.Expert != 163840 {
		t.Fatalf("limits=%+v", got)
	}
}

func TestParseModelInputLimitsRejectsUnknownShape(t *testing.T) {
	if _, err := parseModelInputLimits(map[string]any{"data": map[string]any{"status": "ok"}}); err == nil {
		t.Fatal("expected missing input limits error")
	}
}
