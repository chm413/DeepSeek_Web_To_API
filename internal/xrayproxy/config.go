package xrayproxy

import (
	"encoding/json"
	"fmt"
	"strings"

	"DeepSeek_Web_To_API/internal/proxyuri"
)

type Spec struct {
	ID   string
	Type string
	URI  string
}

func BuildConfig(spec Spec, socksPort int) ([]byte, error) {
	if socksPort < 1 || socksPort > 65535 {
		return nil, fmt.Errorf("invalid local SOCKS port: %d", socksPort)
	}
	node, err := proxyuri.Parse(spec.Type, spec.URI)
	if err != nil {
		return nil, err
	}
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{
				"tag":      "local-socks",
				"listen":   "127.0.0.1",
				"port":     socksPort,
				"protocol": "socks",
				"settings": map[string]any{"auth": "noauth", "udp": false},
			},
		},
		"outbounds": []any{
			node.Outbound,
			map[string]any{"tag": "direct", "protocol": "freedom"},
			map[string]any{"tag": "block", "protocol": "blackhole"},
		},
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"local-socks"}, "outboundTag": "proxy"},
			},
		},
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Xray config: %w", err)
	}
	return encoded, nil
}

func NormalizeSpec(spec Spec) Spec {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.Type = proxyuri.NormalizeType(spec.Type)
	spec.URI = strings.TrimSpace(spec.URI)
	return spec
}
