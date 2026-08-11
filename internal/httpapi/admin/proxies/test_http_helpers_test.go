package proxies

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"DeepSeek_Web_To_API/internal/account"
	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsclient "DeepSeek_Web_To_API/internal/deepseek/client"
	adminconfig "DeepSeek_Web_To_API/internal/httpapi/admin/configmgmt"
	adminshared "DeepSeek_Web_To_API/internal/httpapi/admin/shared"
)

type testingDSMock struct{}

func (m *testingDSMock) Login(_ context.Context, _ config.Account) (string, error) {
	return "token", nil
}
func (m *testingDSMock) CreateSession(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "session-id", nil
}
func (m *testingDSMock) GetPow(_ context.Context, _ *auth.RequestAuth, _ int) (string, error) {
	return "pow", nil
}
func (m *testingDSMock) CallCompletion(_ context.Context, _ *auth.RequestAuth, _ map[string]any, _ string, _ int) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
}
func (m *testingDSMock) DeleteAllSessionsForToken(_ context.Context, _ string) error { return nil }
func (m *testingDSMock) GetSessionCountForToken(_ context.Context, _ string) (*dsclient.SessionStats, error) {
	return &dsclient.SessionStats{}, nil
}

func newHTTPAdminHarness(t *testing.T, rawConfig string, ds adminshared.DeepSeekCaller) http.Handler {
	t.Helper()
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", rawConfig)
	store := config.LoadStore()
	return newHTTPAdminHarnessWithStoreAndDS(store, ds)
}

func newHTTPAdminHarnessWithStore(store *config.Store) http.Handler {
	return newHTTPAdminHarnessWithStoreAndDS(store, &testingDSMock{})
}

func newHTTPAdminHarnessWithStoreAndDS(store *config.Store, ds adminshared.DeepSeekCaller) http.Handler {
	pool := account.NewPool(store)
	h := &Handler{Store: store, Pool: pool, DS: ds}
	configHandler := &adminconfig.Handler{Store: store, Pool: pool, DS: ds}
	r := chi.NewRouter()
	RegisterRoutes(r, h)
	r.Get("/config", configHandler.GetConfig)
	return r
}

func adminReq(method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin")
	req.Header.Set("Content-Type", "application/json")
	return req
}
