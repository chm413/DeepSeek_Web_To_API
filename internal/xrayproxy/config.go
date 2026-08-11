package xrayproxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"DeepSeek_Web_To_API/internal/proxyuri"
)

type Spec struct {
	ID   string
	Type string
	URI  string
}

type Route struct {
	Spec      Spec
	SocksPort int
}

func BuildConfig(spec Spec, socksPort int) ([]byte, error) {
	return BuildConfigMany([]Route{{Spec: spec, SocksPort: socksPort}})
}

func BuildConfigMany(routes []Route) ([]byte, error) {
	if len(routes) == 0 {
		return nil, fmt.Errorf("at least one xray route is required")
	}
	routes = append([]Route(nil), routes...)
	sort.Slice(routes, func(i, j int) bool {
		return NormalizeSpec(routes[i].Spec).ID < NormalizeSpec(routes[j].Spec).ID
	})
	inbounds := make([]any, 0, len(routes))
	outbounds := make([]any, 0, len(routes)+2)
	rules := make([]any, 0, len(routes))
	seenIDs := make(map[string]struct{}, len(routes))
	seenPorts := make(map[int]struct{}, len(routes))
	for _, route := range routes {
		spec := NormalizeSpec(route.Spec)
		if spec.ID == "" {
			return nil, fmt.Errorf("xray route id is required")
		}
		if _, exists := seenIDs[spec.ID]; exists {
			return nil, fmt.Errorf("duplicate xray route id: %s", spec.ID)
		}
		seenIDs[spec.ID] = struct{}{}
		if route.SocksPort < 1 || route.SocksPort > 65535 {
			return nil, fmt.Errorf("invalid local SOCKS port: %d", route.SocksPort)
		}
		if _, exists := seenPorts[route.SocksPort]; exists {
			return nil, fmt.Errorf("duplicate local SOCKS port: %d", route.SocksPort)
		}
		seenPorts[route.SocksPort] = struct{}{}
		node, err := proxyuri.Parse(spec.Type, spec.URI)
		if err != nil {
			return nil, err
		}
		tag := routeTag(spec.ID)
		inboundTag := "local-socks-" + tag
		outboundTag := "proxy-" + tag
		node.Outbound["tag"] = outboundTag
		inbounds = append(inbounds, map[string]any{
			"tag":      inboundTag,
			"listen":   "127.0.0.1",
			"port":     route.SocksPort,
			"protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		})
		outbounds = append(outbounds, node.Outbound)
		rules = append(rules, map[string]any{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"outboundTag": outboundTag,
		})
	}
	outbounds = append(outbounds,
		map[string]any{"tag": "direct", "protocol": "freedom"},
		map[string]any{"tag": "block", "protocol": "blackhole"},
	)
	config := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"routing": map[string]any{
			"domainStrategy": "AsIs",
			"rules":          rules,
		},
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Xray config: %w", err)
	}
	return encoded, nil
}

func routeTag(proxyID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(proxyID)))
	return fmt.Sprintf("%x", sum[:6])
}

func NormalizeSpec(spec Spec) Spec {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.Type = proxyuri.NormalizeType(spec.Type)
	spec.URI = strings.TrimSpace(spec.URI)
	return spec
}
