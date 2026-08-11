package proxysubscription

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/proxyuri"
)

type Result struct {
	Proxies  []config.Proxy
	Invalid  int
	Warnings []string
}

func Parse(body []byte, subscriptionID string) (Result, error) {
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return Result{}, errors.New("subscription id is required")
	}
	result := Result{}
	seen := map[string]struct{}{}
	addURI := func(raw string) {
		proxy, err := proxyFromURI(subscriptionID, raw)
		if err != nil {
			result.Invalid++
			if len(result.Warnings) < 20 {
				result.Warnings = append(result.Warnings, err.Error())
			}
			return
		}
		if _, exists := seen[proxy.ID]; exists {
			return
		}
		seen[proxy.ID] = struct{}{}
		result.Proxies = append(result.Proxies, proxy)
	}

	text := strings.TrimSpace(string(body))
	for _, raw := range subscriptionLines(text) {
		addURI(raw)
	}
	if len(result.Proxies) == 0 {
		if decoded, ok := decodeSubscriptionBase64(text); ok {
			for _, raw := range subscriptionLines(string(decoded)) {
				addURI(raw)
			}
		}
	}
	if len(result.Proxies) == 0 {
		var document struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(body, &document); err == nil {
			for index, item := range document.Proxies {
				raw, err := clashProxyURI(item)
				if err != nil {
					result.Invalid++
					if len(result.Warnings) < 20 {
						result.Warnings = append(result.Warnings, fmt.Sprintf("node %d: %v", index+1, err))
					}
					continue
				}
				addURI(raw)
			}
		}
	}
	if len(result.Proxies) == 0 {
		return result, errors.New("subscription contains no supported VLESS, VMess, or Hysteria2 nodes")
	}
	return result, nil
}

func proxyFromURI(subscriptionID, raw string) (config.Proxy, error) {
	raw = strings.TrimSpace(raw)
	scheme := ""
	if parsed, err := url.Parse(raw); err == nil {
		scheme = proxyuri.NormalizeType(parsed.Scheme)
	}
	if scheme != "vless" && scheme != "vmess" && scheme != "hysteria2" {
		return config.Proxy{}, fmt.Errorf("unsupported subscription node scheme %q", scheme)
	}
	node, err := proxyuri.Parse(scheme, raw)
	if err != nil {
		return config.Proxy{}, err
	}
	proxy := config.NormalizeProxy(config.Proxy{
		ID:             stableNodeID(subscriptionID, scheme, node.Outbound),
		Name:           node.DisplayName,
		Type:           scheme,
		URI:            raw,
		SubscriptionID: subscriptionID,
	})
	if proxy.Name == "" {
		proxy.Name = net.JoinHostPort(node.Address, strconv.Itoa(node.Port))
	}
	return proxy, nil
}

func subscriptionLines(text string) []string {
	lines := strings.FieldsFunc(text, func(r rune) bool { return r == '\r' || r == '\n' })
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "vless://") || strings.HasPrefix(lower, "vmess://") ||
			strings.HasPrefix(lower, "hysteria2://") || strings.HasPrefix(lower, "hy2://") {
			out = append(out, line)
		}
	}
	return out
}

func decodeSubscriptionBase64(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	encodings := []*base64.Encoding{
		base64.RawStdEncoding,
		base64.StdEncoding,
		base64.RawURLEncoding,
		base64.URLEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, true
		}
	}
	return nil, false
}

func clashProxyURI(item map[string]any) (string, error) {
	proxyType := proxyuri.NormalizeType(valueString(item, "type"))
	name := valueString(item, "name")
	server := valueString(item, "server")
	port := valueInt(item, "port")
	if server == "" {
		return "", errors.New("server is required")
	}
	if port == 0 {
		if host, portText, err := net.SplitHostPort(server); err == nil {
			server = host
			port, _ = strconv.Atoi(portText)
		}
	}
	if port < 1 || port > 65535 {
		return "", errors.New("port must be between 1 and 65535")
	}
	if valueBool(item, "skip-cert-verify") {
		return "", errors.New("skip-cert-verify is unsupported by current Xray core")
	}
	switch proxyType {
	case "vless":
		return clashVLESSURI(item, name, server, port)
	case "vmess":
		return clashVMessURI(item, name, server, port)
	case "hysteria2":
		return clashHysteria2URI(item, name, server, port)
	default:
		return "", fmt.Errorf("unsupported Clash proxy type %q", proxyType)
	}
}

func clashVLESSURI(item map[string]any, name, server string, port int) (string, error) {
	id := valueString(item, "uuid")
	if id == "" {
		return "", errors.New("uuid is required")
	}
	u := &url.URL{Scheme: "vless", User: url.User(id), Host: net.JoinHostPort(server, strconv.Itoa(port)), Fragment: name}
	query := url.Values{"encryption": []string{"none"}}
	if flow := valueString(item, "flow"); flow != "" {
		query.Set("flow", flow)
	}
	applyClashTransport(query, item)
	if reality := valueMap(item, "reality-opts"); len(reality) > 0 {
		query.Set("security", "reality")
		query.Set("pbk", firstValue(reality, "public-key", "publicKey"))
		query.Set("sid", firstValue(reality, "short-id", "shortId"))
		query.Set("sni", firstValue(item, "servername", "sni"))
	} else if valueBool(item, "tls") {
		query.Set("security", "tls")
		query.Set("sni", firstValue(item, "servername", "sni"))
	}
	if fp := firstValue(item, "client-fingerprint", "fingerprint"); fp != "" {
		query.Set("fp", fp)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func clashVMessURI(item map[string]any, name, server string, port int) (string, error) {
	id := valueString(item, "uuid")
	if id == "" {
		return "", errors.New("uuid is required")
	}
	network := firstValue(item, "network", "net")
	if network == "" {
		network = "tcp"
	}
	payload := map[string]any{
		"v": "2", "ps": name, "add": server, "port": strconv.Itoa(port),
		"id": id, "aid": valueInt(item, "alterId"), "scy": firstValue(item, "cipher", "security"),
		"net": network,
	}
	query := url.Values{}
	applyClashTransport(query, item)
	payload["host"] = query.Get("host")
	payload["path"] = query.Get("path")
	if valueBool(item, "tls") {
		payload["tls"] = "tls"
		payload["sni"] = firstValue(item, "servername", "sni")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode VMess node: %w", err)
	}
	return "vmess://" + base64.RawStdEncoding.EncodeToString(encoded), nil
}

func clashHysteria2URI(item map[string]any, name, server string, port int) (string, error) {
	auth := firstValue(item, "password", "auth")
	if auth == "" {
		return "", errors.New("password or auth is required")
	}
	if firstValue(item, "obfs", "obfs-password") != "" {
		return "", errors.New("hysteria2 obfs is unsupported by current Xray core")
	}
	u := &url.URL{Scheme: "hysteria2", User: url.User(auth), Host: net.JoinHostPort(server, strconv.Itoa(port)), Fragment: name}
	query := url.Values{}
	if sni := firstValue(item, "sni", "servername"); sni != "" {
		query.Set("sni", sni)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func applyClashTransport(query url.Values, item map[string]any) {
	network := firstValue(item, "network", "net")
	if network == "" {
		network = "tcp"
	}
	query.Set("type", network)
	switch strings.ToLower(network) {
	case "ws":
		options := valueMap(item, "ws-opts")
		query.Set("path", valueString(options, "path"))
		headers := valueMap(options, "headers")
		query.Set("host", firstValue(headers, "Host", "host"))
	case "grpc":
		options := valueMap(item, "grpc-opts")
		query.Set("path", firstValue(options, "grpc-service-name", "service-name"))
	case "xhttp", "splithttp":
		options := valueMap(item, "xhttp-opts")
		query.Set("path", valueString(options, "path"))
		query.Set("host", valueString(options, "host"))
	}
}

func stableNodeID(subscriptionID, proxyType string, outbound map[string]any) string {
	identity, err := json.Marshal(outbound)
	if err != nil {
		identity = []byte(fmt.Sprintf("%v", outbound))
	}
	sum := sha256.Sum256([]byte(subscriptionID + "\x00" + proxyType + "\x00" + string(identity)))
	return fmt.Sprintf("sub_%x", sum[:8])
}

func valueString(item map[string]any, key string) string {
	value, exists := item[key]
	if !exists || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func firstValue(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := valueString(item, key); value != "" {
			return value
		}
	}
	return ""
}

func valueInt(item map[string]any, key string) int {
	value := valueString(item, key)
	result, _ := strconv.Atoi(value)
	return result
}

func valueBool(item map[string]any, key string) bool {
	switch strings.ToLower(valueString(item, key)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func valueMap(item map[string]any, key string) map[string]any {
	if value, ok := item[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}
