package client

import (
	"testing"

	"DeepSeek_Web_To_API/internal/config"
)

func TestResolveProxyForAutomaticAccountRequiresLatestSuccessfulTest(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[
			{"id":"failed","type":"socks5","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":false},
			{"id":"healthy","type":"socks5","host":"127.0.0.1","port":1081,"last_test_at_unix":10,"last_test_success":true}
		]
	}`)
	store := config.LoadStore()
	client := &Client{Store: store}

	if _, _, ok := client.resolveProxyForAccount(config.Account{Email: "auto@example.com", ProxyID: "failed", ProxyAutoRoute: true}); ok {
		t.Fatal("automatic route must reject a node whose latest test failed")
	}
	proxy, _, ok := client.resolveProxyForAccount(config.Account{Email: "auto@example.com", ProxyID: "healthy", ProxyAutoRoute: true})
	if !ok || proxy.ID != "healthy" {
		t.Fatalf("expected healthy automatic route, got %#v, %v", proxy, ok)
	}
}
