package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	dsprotocol "DeepSeek_Web_To_API/internal/deepseek/protocol"
	trans "DeepSeek_Web_To_API/internal/deepseek/transport"
	"DeepSeek_Web_To_API/internal/proxyuri"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

type requestClients struct {
	regular   trans.Doer
	stream    trans.Doer
	fallback  *http.Client
	fallbackS *http.Client
}

type hostLookupFunc func(ctx context.Context, network, host string) ([]string, error)

var proxyConnectivityTestURL = "https://chat.deepseek.com/"

var defaultHostLookup hostLookupFunc = func(ctx context.Context, _ string, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

func proxyDialAddress(ctx context.Context, proxyType, address string, lookup hostLookupFunc) (string, error) {
	proxyType = strings.ToLower(strings.TrimSpace(proxyType))
	if proxyType != "socks5" {
		return address, nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return address, nil
	}
	if lookup == nil {
		lookup = defaultHostLookup
	}
	addrs, err := lookup(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no ip address resolved for %s", host)
	}
	return net.JoinHostPort(addrs[0], port), nil
}

func proxyCacheKey(proxyCfg config.Proxy, coreCfg config.ProxyCoreConfig) string {
	proxyCfg = config.NormalizeProxy(proxyCfg)
	return strings.Join([]string{
		proxyCfg.ID,
		proxyCfg.Type,
		strings.ToLower(proxyCfg.Host),
		strconv.Itoa(proxyCfg.Port),
		proxyCfg.Username,
		proxyCfg.Password,
		proxyCfg.URI,
		coreCfg.XrayBinaryPath,
		coreCfg.RuntimeDir,
		strconv.Itoa(coreCfg.StartupTimeoutSeconds),
	}, "|")
}

func proxyDialContext(proxyCfg config.Proxy, coreCfg config.ProxyCoreConfig) (trans.DialContextFunc, error) {
	proxyCfg = config.NormalizeProxy(proxyCfg)
	if proxyuri.IsCoreType(proxyCfg.Type) {
		if _, err := proxyuri.Parse(proxyCfg.Type, proxyCfg.URI); err != nil {
			return nil, err
		}
		spec := xrayproxy.Spec{ID: proxyCfg.ID, Type: proxyCfg.Type, URI: proxyCfg.URI}
		settings := xrayproxy.Settings{
			BinaryPath:     coreCfg.XrayBinaryPath,
			RuntimeDir:     coreCfg.RuntimeDir,
			StartupTimeout: time.Duration(coreCfg.StartupTimeoutSeconds) * time.Second,
		}
		return func(ctx context.Context, network, address string) (net.Conn, error) {
			localAddress, err := xrayproxy.Default().Ensure(ctx, spec, settings)
			if err != nil {
				return nil, err
			}
			return dialSOCKS(ctx, "socks5h", localAddress, nil, network, address)
		}, nil
	}
	var authCfg *proxy.Auth
	if proxyCfg.Username != "" || proxyCfg.Password != "" {
		authCfg = &proxy.Auth{User: proxyCfg.Username, Password: proxyCfg.Password}
	}
	proxyAddress := net.JoinHostPort(proxyCfg.Host, strconv.Itoa(proxyCfg.Port))
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialSOCKS(ctx, proxyCfg.Type, proxyAddress, authCfg, network, address)
	}, nil
}

func dialSOCKS(ctx context.Context, proxyType, proxyAddress string, authCfg *proxy.Auth, network, address string) (net.Conn, error) {
	forward := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	dialer, err := proxy.SOCKS5("tcp", proxyAddress, authCfg, forward)
	if err != nil {
		return nil, err
	}
	target, err := proxyDialAddress(ctx, proxyType, address, defaultHostLookup)
	if err != nil {
		return nil, err
	}
	if ctxDialer, ok := dialer.(proxy.ContextDialer); ok {
		return ctxDialer.DialContext(ctx, network, target)
	}
	return dialer.Dial(network, target)
}

func (c *Client) defaultRequestClients() requestClients {
	return requestClients{
		regular:   c.regular,
		stream:    c.stream,
		fallback:  c.fallback,
		fallbackS: c.fallbackS,
	}
}

func (c *Client) resolveProxyForAccount(acc config.Account) (config.Proxy, config.ProxyCoreConfig, bool) {
	if c == nil || c.Store == nil {
		return config.Proxy{}, config.ProxyCoreConfig{}, false
	}
	proxyID := strings.TrimSpace(acc.ProxyID)
	if proxyID == "" {
		return config.Proxy{}, config.ProxyCoreConfig{}, false
	}
	snap := c.Store.Snapshot()
	for _, proxyCfg := range snap.Proxies {
		proxyCfg = config.NormalizeProxy(proxyCfg)
		if proxyCfg.ID == proxyID {
			return proxyCfg, snap.ProxyCore, true
		}
	}
	return config.Proxy{}, snap.ProxyCore, false
}

func (c *Client) requestClientsFromContext(ctx context.Context) requestClients {
	if a, ok := auth.FromContext(ctx); ok {
		return c.requestClientsForAccount(a.Account)
	}
	return c.defaultRequestClients()
}

func (c *Client) requestClientsForAuth(ctx context.Context, a *auth.RequestAuth) requestClients {
	if a != nil {
		return c.requestClientsForAccount(a.Account)
	}
	return c.requestClientsFromContext(ctx)
}

func (c *Client) requestClientsForAccount(acc config.Account) requestClients {
	proxyCfg, coreCfg, ok := c.resolveProxyForAccount(acc)
	if !ok {
		return c.defaultRequestClients()
	}

	key := proxyCacheKey(proxyCfg, coreCfg)
	c.proxyClientsMu.RLock()
	cached, ok := c.proxyClients[key]
	c.proxyClientsMu.RUnlock()
	if ok {
		return cached
	}

	dialContext, err := proxyDialContext(proxyCfg, coreCfg)
	if err != nil {
		config.Logger.Warn("[proxy] build dialer failed", "proxy_id", proxyCfg.ID, "error", err)
		dialContext = func(context.Context, string, string) (net.Conn, error) {
			return nil, fmt.Errorf("proxy %s is unavailable: %w", proxyCfg.ID, err)
		}
	}
	totalTimeout := config.HTTPTotalTimeout()
	if c.Store != nil {
		totalTimeout = c.Store.HTTPTotalTimeout()
	}

	bundle := requestClients{
		regular:   trans.NewWithDialContext(totalTimeout, dialContext),
		stream:    trans.NewWithDialContext(0, dialContext),
		fallback:  trans.NewFallbackClient(totalTimeout, dialContext),
		fallbackS: trans.NewFallbackClient(0, dialContext),
	}

	c.proxyClientsMu.Lock()
	if c.proxyClients == nil {
		c.proxyClients = make(map[string]requestClients)
	}
	c.proxyClients[key] = bundle
	c.proxyClientsMu.Unlock()
	return bundle
}

func applyProxyConnectivityHeaders(req *http.Request) {
	if req == nil {
		return
	}
	for key, value := range dsprotocol.BaseHeaders {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func proxyConnectivityStatus(statusCode int) (bool, string) {
	switch {
	case statusCode >= 200 && statusCode < 300:
		return true, fmt.Sprintf("代理可达，目标返回 HTTP %d", statusCode)
	case statusCode >= 300 && statusCode < 500:
		return true, fmt.Sprintf("代理可达，但目标返回 HTTP %d（可能是风控或挑战）", statusCode)
	default:
		return false, fmt.Sprintf("目标返回 HTTP %d", statusCode)
	}
}

func TestProxyConnectivity(ctx context.Context, proxyCfg config.Proxy) map[string]any {
	return TestProxyConnectivityWithCore(ctx, proxyCfg, config.ProxyCoreConfig{})
}

func TestProxyConnectivityWithCore(ctx context.Context, proxyCfg config.Proxy, coreCfg config.ProxyCoreConfig) map[string]any {
	start := time.Now()
	proxyCfg = config.NormalizeProxy(proxyCfg)
	result := map[string]any{
		"success":       false,
		"proxy_id":      proxyCfg.ID,
		"proxy_type":    proxyCfg.Type,
		"response_time": 0,
	}

	if err := config.ValidateProxyConfig([]config.Proxy{proxyCfg}); err != nil {
		result["message"] = "代理配置无效: " + err.Error()
		return result
	}
	dialContext, err := proxyDialContext(proxyCfg, coreCfg)
	if err != nil {
		result["message"] = "代理拨号器初始化失败: " + err.Error()
		return result
	}

	client := trans.NewFallbackClient(15*time.Second, dialContext)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyConnectivityTestURL, nil)
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	applyProxyConnectivityHeaders(req)

	resp, err := client.Do(req)
	result["response_time"] = int(time.Since(start).Milliseconds())
	if err != nil {
		result["message"] = err.Error()
		return result
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			config.Logger.Warn("[proxy] close response body failed", "proxy_id", proxyCfg.ID, "error", closeErr)
		}
	}()

	result["status_code"] = resp.StatusCode
	result["success"], result["message"] = proxyConnectivityStatus(resp.StatusCode)
	return result
}
