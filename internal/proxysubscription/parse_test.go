package proxysubscription

import (
	"encoding/base64"
	"testing"
)

const testVLESS = "vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls&sni=example.com#VLESS"

func TestParsePlainAndBase64Subscription(t *testing.T) {
	plain := testVLESS + "\n" + "hy2://secret@example.com:8443?sni=example.com#HY2"
	for name, body := range map[string][]byte{
		"plain":  []byte(plain),
		"base64": []byte(base64.StdEncoding.EncodeToString([]byte(plain))),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Parse(body, "subscription-1")
			if err != nil {
				t.Fatalf("parse subscription: %v", err)
			}
			if len(result.Proxies) != 2 || result.Invalid != 0 {
				t.Fatalf("unexpected result: %#v", result)
			}
			for _, proxy := range result.Proxies {
				if proxy.SubscriptionID != "subscription-1" || proxy.ID == "" || proxy.URI == "" {
					t.Fatalf("unexpected proxy: %#v", proxy)
				}
			}
		})
	}
}

func TestParseKeepsStableNodeIDWhenDisplayNameChanges(t *testing.T) {
	first, err := Parse([]byte("vless://11111111-1111-1111-1111-111111111111@example.com:443?encryption=none&security=tls#Old%20name"), "sub-1")
	if err != nil {
		t.Fatalf("parse first node: %v", err)
	}
	second, err := Parse([]byte("vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls&encryption=none#New%20name"), "sub-1")
	if err != nil {
		t.Fatalf("parse renamed node: %v", err)
	}
	if first.Proxies[0].ID != second.Proxies[0].ID {
		t.Fatalf("display name or query order changed stable id: %s != %s", first.Proxies[0].ID, second.Proxies[0].ID)
	}
}

func TestParseClashYAML(t *testing.T) {
	body := []byte(`
proxies:
  - name: Clash VLESS
    type: vless
    server: edge.example.com
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
    network: ws
    tls: true
    servername: edge.example.com
    ws-opts:
      path: /ws
      headers:
        Host: cdn.example.com
  - name: Clash HY2
    type: hysteria2
    server: hy.example.com
    port: 8443
    password: secret
    sni: hy.example.com
`)
	result, err := Parse(body, "subscription-2")
	if err != nil {
		t.Fatalf("parse Clash subscription: %v", err)
	}
	if len(result.Proxies) != 2 || result.Invalid != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Proxies[0].Name != "Clash VLESS" || result.Proxies[1].Name != "Clash HY2" {
		t.Fatalf("unexpected names: %#v", result.Proxies)
	}
}

func TestParseClashRejectsInsecureNodeButKeepsValidNodes(t *testing.T) {
	body := []byte(`
proxies:
  - name: Insecure
    type: vless
    server: bad.example.com
    port: 443
    uuid: 11111111-1111-1111-1111-111111111111
    tls: true
    skip-cert-verify: true
  - name: Valid
    type: vless
    server: good.example.com
    port: 443
    uuid: 22222222-2222-2222-2222-222222222222
    tls: true
    servername: good.example.com
`)
	result, err := Parse(body, "subscription-3")
	if err != nil {
		t.Fatalf("parse partial subscription: %v", err)
	}
	if len(result.Proxies) != 1 || result.Invalid != 1 {
		t.Fatalf("unexpected partial result: %#v", result)
	}
}
