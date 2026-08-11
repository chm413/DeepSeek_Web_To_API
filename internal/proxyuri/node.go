package proxyuri

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

type Node struct {
	Type        string
	Address     string
	Port        int
	DisplayName string
	Outbound    map[string]any
}

func NormalizeType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hy2", "hysteria2":
		return "hysteria2"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func IsCoreType(value string) bool {
	switch NormalizeType(value) {
	case "vless", "vmess", "hysteria2":
		return true
	default:
		return false
	}
}

func Parse(proxyType, rawURI string) (Node, error) {
	proxyType = NormalizeType(proxyType)
	rawURI = strings.TrimSpace(rawURI)
	if rawURI == "" {
		return Node{}, errors.New("proxy node URI is required")
	}
	switch proxyType {
	case "vless":
		return parseVLESS(rawURI)
	case "vmess":
		return parseVMess(rawURI)
	case "hysteria2":
		return parseHysteria2(rawURI)
	default:
		return Node{}, fmt.Errorf("unsupported core proxy type: %s", proxyType)
	}
}

func parseVLESS(rawURI string) (Node, error) {
	u, err := url.Parse(rawURI)
	if err != nil || !strings.EqualFold(u.Scheme, "vless") {
		return Node{}, errors.New("invalid VLESS URI")
	}
	address, port, err := endpoint(u)
	if err != nil {
		return Node{}, fmt.Errorf("invalid VLESS endpoint: %w", err)
	}
	id := ""
	if u.User != nil {
		id = strings.TrimSpace(u.User.Username())
	}
	if _, err := uuid.Parse(id); err != nil {
		return Node{}, errors.New("invalid VLESS UUID")
	}
	query := u.Query()
	encryption := strings.TrimSpace(query.Get("encryption"))
	if encryption == "" {
		encryption = "none"
	}
	settings := map[string]any{
		"address":    address,
		"port":       port,
		"id":         id,
		"encryption": encryption,
	}
	if flow := strings.TrimSpace(query.Get("flow")); flow != "" {
		settings["flow"] = flow
	}
	stream, err := buildStreamSettings(query, "tcp", "")
	if err != nil {
		return Node{}, fmt.Errorf("invalid VLESS stream settings: %w", err)
	}
	return Node{
		Type:        "vless",
		Address:     address,
		Port:        port,
		DisplayName: fragmentName(u),
		Outbound: map[string]any{
			"tag":            "proxy",
			"protocol":       "vless",
			"settings":       settings,
			"streamSettings": stream,
		},
	}, nil
}

type vmessLink struct {
	Version  any    `json:"v"`
	Name     string `json:"ps"`
	Address  string `json:"add"`
	Port     any    `json:"port"`
	ID       string `json:"id"`
	AlterID  any    `json:"aid"`
	Security string `json:"scy"`
	Network  string `json:"net"`
	Header   string `json:"type"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	TLS      string `json:"tls"`
	SNI      string `json:"sni"`
	ALPN     string `json:"alpn"`
	FP       string `json:"fp"`
}

func parseVMess(rawURI string) (Node, error) {
	if !strings.HasPrefix(strings.ToLower(rawURI), "vmess://") {
		return Node{}, errors.New("invalid VMess URI")
	}
	payload, err := decodeBase64(strings.TrimSpace(rawURI[len("vmess://"):]))
	if err != nil {
		return Node{}, errors.New("invalid VMess base64 payload")
	}
	var link vmessLink
	if err := json.Unmarshal(payload, &link); err != nil {
		return Node{}, errors.New("invalid VMess JSON payload")
	}
	address := strings.TrimSpace(link.Address)
	if address == "" {
		return Node{}, errors.New("vmess address is required")
	}
	port, err := flexibleInt(link.Port)
	if err != nil || port < 1 || port > 65535 {
		return Node{}, errors.New("invalid VMess port")
	}
	if _, err := uuid.Parse(strings.TrimSpace(link.ID)); err != nil {
		return Node{}, errors.New("invalid VMess UUID")
	}
	if aid, _ := flexibleInt(link.AlterID); aid != 0 {
		return Node{}, errors.New("vmess alterId must be 0 for current Xray core")
	}
	security := strings.TrimSpace(link.Security)
	if security == "" {
		security = "auto"
	}
	query := url.Values{}
	query.Set("type", firstNonEmpty(link.Network, "tcp"))
	query.Set("security", link.TLS)
	query.Set("host", link.Host)
	query.Set("path", link.Path)
	query.Set("headerType", link.Header)
	query.Set("sni", link.SNI)
	query.Set("alpn", link.ALPN)
	query.Set("fp", link.FP)
	stream, err := buildStreamSettings(query, "tcp", link.TLS)
	if err != nil {
		return Node{}, fmt.Errorf("invalid VMess stream settings: %w", err)
	}
	return Node{
		Type:        "vmess",
		Address:     address,
		Port:        port,
		DisplayName: strings.TrimSpace(link.Name),
		Outbound: map[string]any{
			"tag":      "proxy",
			"protocol": "vmess",
			"settings": map[string]any{
				"address":  address,
				"port":     port,
				"id":       strings.TrimSpace(link.ID),
				"security": security,
			},
			"streamSettings": stream,
		},
	}, nil
}

func parseHysteria2(rawURI string) (Node, error) {
	u, err := url.Parse(rawURI)
	if err != nil {
		return Node{}, errors.New("invalid Hysteria2 URI")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "hysteria2" && scheme != "hy2" {
		return Node{}, errors.New("invalid Hysteria2 URI")
	}
	address, port, err := endpoint(u)
	if err != nil {
		return Node{}, fmt.Errorf("invalid Hysteria2 endpoint: %w", err)
	}
	auth := ""
	if u.User != nil {
		auth = u.User.Username()
		if password, ok := u.User.Password(); ok {
			auth += ":" + password
		}
	}
	if strings.TrimSpace(auth) == "" {
		return Node{}, errors.New("hysteria2 auth is required")
	}
	query := u.Query()
	if query.Get("obfs") != "" || query.Get("obfs-password") != "" || query.Get("obfsPassword") != "" {
		return Node{}, errors.New("hysteria2 obfs parameters are not supported by Xray core")
	}
	if query.Get("pinSHA256") != "" {
		return Node{}, errors.New("hysteria2 pinSHA256 is not supported by this integration")
	}
	query.Set("type", "hysteria")
	query.Set("security", "tls")
	stream, err := buildStreamSettings(query, "hysteria", "tls")
	if err != nil {
		return Node{}, fmt.Errorf("invalid Hysteria2 stream settings: %w", err)
	}
	stream["hysteriaSettings"] = map[string]any{"version": 2, "auth": auth}
	return Node{
		Type:        "hysteria2",
		Address:     address,
		Port:        port,
		DisplayName: fragmentName(u),
		Outbound: map[string]any{
			"tag":      "proxy",
			"protocol": "hysteria",
			"settings": map[string]any{
				"version": 2,
				"address": address,
				"port":    port,
			},
			"streamSettings": stream,
		},
	}, nil
}

func buildStreamSettings(query url.Values, defaultNetwork, defaultSecurity string) (map[string]any, error) {
	network := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("network"), defaultNetwork)))
	switch network {
	case "", "tcp", "raw":
		network = "tcp"
	case "ws", "websocket":
		network = "ws"
	case "grpc", "xhttp", "splithttp", "httpupgrade", "hysteria":
	default:
		return nil, fmt.Errorf("unsupported transport %q", network)
	}
	security := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("security"), defaultSecurity)))
	if security == "none" {
		security = ""
	}
	if security != "" && security != "tls" && security != "reality" {
		return nil, fmt.Errorf("unsupported security %q", security)
	}
	stream := map[string]any{"network": network, "security": security}

	host := strings.TrimSpace(query.Get("host"))
	path := strings.TrimSpace(query.Get("path"))
	switch network {
	case "tcp":
		headerType := strings.ToLower(strings.TrimSpace(query.Get("headerType")))
		if headerType != "" && headerType != "none" {
			return nil, fmt.Errorf("unsupported TCP header type %q", headerType)
		}
	case "ws":
		settings := map[string]any{"path": firstNonEmpty(path, "/")}
		if host != "" {
			settings["headers"] = map[string]any{"Host": host}
		}
		stream["wsSettings"] = settings
	case "grpc":
		stream["grpcSettings"] = map[string]any{"serviceName": strings.TrimPrefix(path, "/")}
	case "xhttp", "splithttp":
		settings := map[string]any{"path": firstNonEmpty(path, "/")}
		if host != "" {
			settings["host"] = host
		}
		if mode := strings.TrimSpace(query.Get("mode")); mode != "" {
			settings["mode"] = mode
		}
		stream["xhttpSettings"] = settings
	case "httpupgrade":
		settings := map[string]any{"path": firstNonEmpty(path, "/")}
		if host != "" {
			settings["host"] = host
		}
		stream["httpupgradeSettings"] = settings
	}

	if security == "tls" {
		if parseBool(query.Get("allowInsecure")) || parseBool(query.Get("insecure")) {
			return nil, errors.New("tls allowInsecure/insecure is not supported by current Xray core; use a valid certificate")
		}
		tlsSettings := map[string]any{}
		if sni := strings.TrimSpace(firstNonEmpty(query.Get("sni"), query.Get("serverName"))); sni != "" {
			tlsSettings["serverName"] = sni
		}
		if fingerprint := strings.TrimSpace(query.Get("fp")); fingerprint != "" {
			tlsSettings["fingerprint"] = fingerprint
		}
		if alpn := splitList(query.Get("alpn")); len(alpn) > 0 {
			tlsSettings["alpn"] = alpn
		}
		stream["tlsSettings"] = tlsSettings
	}
	if security == "reality" {
		reality := map[string]any{}
		for key, value := range map[string]string{
			"serverName":  firstNonEmpty(query.Get("sni"), query.Get("serverName")),
			"fingerprint": query.Get("fp"),
			"publicKey":   firstNonEmpty(query.Get("pbk"), query.Get("publicKey")),
			"shortId":     firstNonEmpty(query.Get("sid"), query.Get("shortId")),
			"spiderX":     firstNonEmpty(query.Get("spx"), query.Get("spiderX")),
		} {
			if strings.TrimSpace(value) != "" {
				reality[key] = strings.TrimSpace(value)
			}
		}
		if reality["publicKey"] == nil {
			return nil, errors.New("reality public key is required")
		}
		stream["realitySettings"] = reality
	}
	return stream, nil
}

func endpoint(u *url.URL) (string, int, error) {
	address := strings.TrimSpace(u.Hostname())
	if address == "" {
		return "", 0, errors.New("address is required")
	}
	portText := strings.TrimSpace(u.Port())
	if portText == "" {
		return "", 0, errors.New("port is required")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("port must be between 1 and 65535")
	}
	return address, port, nil
}

func fragmentName(u *url.URL) string {
	name, err := url.QueryUnescape(strings.TrimSpace(u.Fragment))
	if err != nil {
		return strings.TrimSpace(u.Fragment)
	}
	return strings.TrimSpace(name)
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	encodings := []*base64.Encoding{
		base64.RawStdEncoding,
		base64.StdEncoding,
		base64.RawURLEncoding,
		base64.URLEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func flexibleInt(value any) (int, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return int(typed), nil
	case json.Number:
		return strconv.Atoi(typed.String())
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, nil
		}
		return strconv.Atoi(strings.TrimSpace(typed))
	default:
		return 0, fmt.Errorf("invalid integer value")
	}
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '|' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func JoinHostPort(address string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(address), strconv.Itoa(port))
}
