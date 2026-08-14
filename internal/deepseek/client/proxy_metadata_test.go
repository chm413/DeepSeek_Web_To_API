package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyExitMetadataParsesCloudflareTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("ip=203.0.113.9\nloc=jp\ncolo=nrt\n")); err != nil {
			t.Errorf("write trace response: %v", err)
		}
	}))
	defer server.Close()
	original := proxyGeoTraceURL
	proxyGeoTraceURL = server.URL
	t.Cleanup(func() { proxyGeoTraceURL = original })

	metadata := proxyExitMetadata(context.Background(), server.Client())
	if metadata["exit_ip"] != "203.0.113.9" || metadata["country"] != "JP" || metadata["colo"] != "NRT" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}
