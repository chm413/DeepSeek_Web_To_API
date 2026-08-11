package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
)

type testingDSMock struct {
	loginCalls                 int
	loginErr                   error
	createSessionCalls         int
	getPowCalls                int
	callCompletionCalls        int
	deleteAllSessionsCalls     int
	deleteAllSessionsError     error
	deleteAllSessionsErrorOnce bool
	sessionCount               int
}

type failingLoginTestingDS struct {
	*testingDSMock
	loginErr error
}

func (m *failingLoginTestingDS) Login(_ context.Context, _ config.Account) (string, error) {
	m.loginCalls++
	return "", m.loginErr
}

func (m *testingDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	m.loginCalls++
	if m.loginErr != nil {
		return "", m.loginErr
	}
	return "new-token", nil
}

func (m *testingDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.createSessionCalls++
	return "session-id", nil
}

func (m *testingDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	m.getPowCalls++
	return "", errors.New("should not call GetPow in this test")
}

func (m *testingDSMock) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	m.callCompletionCalls++
	return nil, errors.New("should not call CallCompletion in this test")
}

func (m *testingDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	m.deleteAllSessionsCalls++
	if m.deleteAllSessionsError != nil {
		err := m.deleteAllSessionsError
		if m.deleteAllSessionsErrorOnce {
			m.deleteAllSessionsError = nil
		}
		return err
	}
	return nil
}

func (m *testingDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{FirstPageCount: m.sessionCount, Success: true}, nil
}

func TestTestAccount_BatchModeOnlyCreatesSession(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":""}]}`)
	store := config.LoadStore()
	ds := &testingDSMock{sessionCount: 7}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	msg, _ := result["message"].(string)
	if !strings.Contains(msg, "Token 刷新成功") {
		t.Fatalf("expected session-only success message, got %q", msg)
	}
	if ds.loginCalls != 1 || ds.createSessionCalls != 1 {
		t.Fatalf("unexpected Login/CreateSession calls: login=%d createSession=%d", ds.loginCalls, ds.createSessionCalls)
	}
	if ds.getPowCalls != 0 || ds.callCompletionCalls != 0 {
		t.Fatalf("expected no completion flow calls, got getPow=%d callCompletion=%d", ds.getPowCalls, ds.callCompletionCalls)
	}
	updated, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected updated account")
	}
	if updated.Token != "new-token" {
		t.Fatalf("expected refreshed token to be persisted, got %q", updated.Token)
	}
	testStatus, ok := store.AccountTestStatus("batch@example.com")
	if !ok || testStatus != "ok" {
		t.Fatalf("expected runtime test status ok, got %q (ok=%v)", testStatus, ok)
	}
	sessionCount, ok := store.AccountSessionCount("batch@example.com")
	if !ok || sessionCount != 7 {
		t.Fatalf("expected runtime session count 7, got %d (ok=%v)", sessionCount, ok)
	}
}

func TestTestAccountPersistsTokenRefreshFailureDetails(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd"}]}`)
	store := config.LoadStore()
	ds := &failingLoginTestingDS{
		testingDSMock: &testingDSMock{},
		loginErr: &auth.AccountHealthError{
			State:   account.HealthPermanentlyBanned,
			Code:    40012,
			Message: "upstream account ban",
		},
	}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-flash", "")
	if success, _ := result["success"].(bool); success {
		t.Fatalf("expected refresh failure, got %#v", result)
	}
	if phase, _ := result["phase"].(string); phase != "token_refresh" {
		t.Fatalf("failure phase = %q, want token_refresh", phase)
	}
	if reason, _ := result["failure_reason"].(string); reason != "upstream account ban" {
		t.Fatalf("failure reason = %q", reason)
	}
	if code, _ := result["error_code"].(int); code != 40012 {
		t.Fatalf("error code = %d, want 40012", code)
	}
	persisted, ok := store.AccountTestResult("batch@example.com")
	if !ok || persisted.Phase != "token_refresh" || persisted.FailureReason != "upstream account ban" || persisted.ErrorCode != 40012 {
		t.Fatalf("persisted test failure = %#v (ok=%v)", persisted, ok)
	}
}

func TestDeleteAllSessions_RetryWithReloginOnDeleteFailure(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"expired-token"}]}`)
	store := config.LoadStore()
	ds := &testingDSMock{deleteAllSessionsError: errors.New("token expired"), deleteAllSessionsErrorOnce: true}
	h := &Handler{Store: store, DS: ds}

	req := httptest.NewRequest(http.MethodPost, "/delete-all", bytes.NewBufferString(`{"identifier":"batch@example.com"}`))
	rec := httptest.NewRecorder()
	h.deleteAllSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["success"].(bool); !ok {
		t.Fatalf("expected success response, got %#v", resp)
	}
	if ds.loginCalls != 2 {
		t.Fatalf("expected initial login plus relogin, got %d", ds.loginCalls)
	}
	if ds.deleteAllSessionsCalls != 2 {
		t.Fatalf("expected delete called twice, got %d", ds.deleteAllSessionsCalls)
	}
	updated, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected account")
	}
	if updated.Token != "new-token" {
		t.Fatalf("expected refreshed token persisted, got %q", updated.Token)
	}
	sessionCount, ok := store.AccountSessionCount("batch@example.com")
	if !ok || sessionCount != 0 {
		t.Fatalf("expected runtime session count reset to 0, got %d (ok=%v)", sessionCount, ok)
	}
}

func TestDeleteAllSessionsLoginFailureAutomaticallyDisablesInvalidCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"invalid@example.com","password":"wrong"}]}`)
	store := config.LoadStore()
	pool := account.NewPool(store)
	ds := &testingDSMock{loginErr: &auth.AccountHealthError{
		State:   account.HealthInvalidCredentials,
		Message: "password rejected",
	}}
	h := &Handler{Store: store, Pool: pool, DS: ds}
	req := httptest.NewRequest(http.MethodPost, "/delete-all", bytes.NewBufferString(`{"identifier":"invalid@example.com"}`))
	rec := httptest.NewRecorder()
	h.deleteAllSessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if autoDisabled, _ := response["auto_disabled"].(bool); !autoDisabled {
		t.Fatalf("expected auto_disabled response, got %#v", response)
	}
	acc, ok := store.FindAccount("invalid@example.com")
	if !ok || !acc.Disabled || acc.DisabledReason != config.AccountDisabledInvalidCredentials {
		t.Fatalf("expected invalid account to be disabled, got %#v, %v", acc, ok)
	}
}

type completionPayloadDSMock struct {
	payload map[string]any
}

func (m *completionPayloadDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	return "new-token", nil
}

func (m *completionPayloadDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}

func (m *completionPayloadDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow-ok", nil
}

func (m *completionPayloadDSMock) CallCompletion(_ context.Context, _ *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.payload = payload
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: {\"v\":\"ok\"}\n\ndata: [DONE]\n\n")),
	}, nil
}

func (m *completionPayloadDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func (m *completionPayloadDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{Success: true}, nil
}

func TestTestAccount_MessageModeUsesExpertModelTypeForExpertModel(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"seed-token"}]}`)
	store := config.LoadStore()
	ds := &completionPayloadDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-pro", "hello")

	if ok, _ := result["success"].(bool); !ok {
		t.Fatalf("expected success=true, got %#v", result)
	}
	if got := ds.payload["model_type"]; got != "expert" {
		t.Fatalf("expected model_type expert, got %#v", got)
	}
	if got := ds.payload["chat_session_id"]; got != "session-id" {
		t.Fatalf("unexpected chat_session_id: %#v", got)
	}
}

// v1.0.10: deepseek-v4-vision was disabled. The admin "test account" path
// must reject the request rather than proxy to a banned model.
func TestTestAccount_RejectsDisabledVisionModel(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"accounts":[{"email":"batch@example.com","password":"pwd","token":"seed-token"}]}`)
	store := config.LoadStore()
	ds := &completionPayloadDSMock{}
	h := &Handler{Store: store, DS: ds}
	acc, ok := store.FindAccount("batch@example.com")
	if !ok {
		t.Fatal("expected test account")
	}

	result := h.testAccount(context.Background(), acc, "deepseek-v4-vision", "hello")

	if ok, _ := result["success"].(bool); ok {
		t.Fatalf("expected success=false for disabled vision model, got %#v", result)
	}
}

type completionAPIDSMock struct {
	payload   map[string]any
	usedToken string
	callCount int
}

func (m *completionAPIDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	return "api-token", nil
}

func (m *completionAPIDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-api", nil
}

func (m *completionAPIDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow-api", nil
}

func (m *completionAPIDSMock) CallCompletion(_ context.Context, a *auth.RequestAuth, payload map[string]any, _ string, _ int) (*http.Response, error) {
	m.payload = payload
	m.usedToken = a.DeepSeekToken
	m.callCount++
	body := "data: {\"p\":\"response/content\",\"v\":\"api-test-answer\"}\n\ndata: [DONE]\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (m *completionAPIDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error {
	return nil
}

func (m *completionAPIDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{Success: true}, nil
}

func TestTestAPI_FallbackToConfiguredAPIKeyWhenNotProvided(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["configured-key"]}`)
	store := config.LoadStore()
	ds := &completionAPIDSMock{}
	h := &Handler{Store: store, DS: ds}

	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"system","content":"ignore"},
			{"role":"user","content":"你好"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.testAPI(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ok, _ := resp["success"].(bool); !ok {
		t.Fatalf("expected success response, got %#v", resp)
	}
	if ds.callCount != 1 {
		t.Fatalf("expected CallCompletion 1x, got %d", ds.callCount)
	}
	if ds.usedToken != "configured-key" {
		t.Fatalf("expected fallback key used, got %q", ds.usedToken)
	}
	response, ok := resp["response"].(map[string]any)
	if !ok {
		t.Fatalf("expected response map, got %#v", resp["response"])
	}
	if got := response["text"]; got != "api-test-answer" {
		t.Fatalf("expected response text, got %#v", got)
	}
}

func TestTestAPI_UsesProvidedApiKeyFromPayload(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{"keys":["configured-key"]}`)
	store := config.LoadStore()
	ds := &completionAPIDSMock{}
	h := &Handler{Store: store, DS: ds}
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"message":"hello","api_key":"custom-key"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.testAPI(rec, req)

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if ds.usedToken != "custom-key" {
		t.Fatalf("expected custom api key used, got %q", ds.usedToken)
	}
}
