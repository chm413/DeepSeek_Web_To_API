package client

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"DeepSeek_Web_To_API/internal/config"
)

func TestResolveProxyForAutomaticAccountRequiresLatestSuccessfulTest(t *testing.T) {
	now := time.Now().Unix()
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[
			{"id":"failed","type":"socks5","host":"127.0.0.1","port":1080,"last_test_at_unix":`+strconv.FormatInt(now, 10)+`,"last_test_success":false},
			{"id":"healthy","type":"socks5","host":"127.0.0.1","port":1081,"last_test_at_unix":`+strconv.FormatInt(now, 10)+`,"last_test_success":true}
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

func TestResolveProxyForAutomaticAccountRejectsStaleSuccessfulTest(t *testing.T) {
	stale := time.Now().Add(-31 * time.Minute).Unix()
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[{"id":"stale","type":"socks5","host":"127.0.0.1","port":1080,"last_test_at_unix":`+strconv.FormatInt(stale, 10)+`,"last_test_success":true}]
	}`)
	client := &Client{Store: config.LoadStore()}
	if _, _, ok := client.resolveProxyForAccount(config.Account{Email: "auto@example.com", ProxyID: "stale", ProxyAutoRoute: true}); ok {
		t.Fatal("automatic route must reject a stale successful probe")
	}
}

func TestAutomaticAccountWithoutHealthyRouteFailsClosed(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[{"id":"failed","type":"socks5","host":"127.0.0.1","port":1080,"last_test_at_unix":10,"last_test_success":false}]
	}`)
	client := &Client{Store: config.LoadStore()}
	request, err := http.NewRequest(http.MethodGet, "https://chat.deepseek.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.requestClientsForAccount(config.Account{Email: "auto@example.com", ProxyID: "failed", ProxyAutoRoute: true}).regular.Do(request)
	if err == nil || !strings.Contains(err.Error(), "waiting for an available node") {
		t.Fatalf("automatic account without a route must fail closed, err=%v", err)
	}
}

func TestManuallyAssignedUnavailableRouteFailsClosed(t *testing.T) {
	t.Setenv("DEEPSEEK_WEB_TO_API_CONFIG_JSON", `{
		"proxies":[{"id":"disabled","type":"socks5","host":"127.0.0.1","port":1080,"disabled":true}]
	}`)
	client := &Client{Store: config.LoadStore()}
	request, err := http.NewRequest(http.MethodGet, "https://chat.deepseek.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.requestClientsForAccount(config.Account{Email: "manual@example.com", ProxyID: "disabled"}).regular.Do(request)
	if err == nil || !strings.Contains(err.Error(), "assigned proxy route") {
		t.Fatalf("manually assigned unavailable route must fail closed, err=%v", err)
	}
}
