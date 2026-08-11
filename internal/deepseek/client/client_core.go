package client

import (
	"context"
	"net/http"
	"sync"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	trans "DeepSeek_Web_To_API/internal/deepseek/transport"
	"DeepSeek_Web_To_API/internal/devcapture"
	"DeepSeek_Web_To_API/internal/util"
)

// intFrom is a package-internal alias for the shared util version.
var intFrom = util.IntFrom

type Client struct {
	Store      *config.Store
	Auth       *auth.Resolver
	capture    *devcapture.Store
	regular    trans.Doer
	stream     trans.Doer
	fallback   *http.Client
	fallbackS  *http.Client
	maxRetries int

	proxyClientsMu sync.RWMutex
	proxyClients   map[string]requestClients

	modelInputLimitsMu sync.Mutex
	modelInputLimits   map[string]cachedModelInputLimits
}

func NewClient(store *config.Store, resolver *auth.Resolver) *Client {
	totalTimeout := config.HTTPTotalTimeout()
	if store != nil {
		totalTimeout = store.HTTPTotalTimeout()
	}
	return &Client{
		Store:        store,
		Auth:         resolver,
		capture:      devcapture.Global(),
		regular:      trans.New(totalTimeout),
		stream:       trans.New(0),
		fallback:     &http.Client{Timeout: totalTimeout},
		fallbackS:    &http.Client{Timeout: 0},
		maxRetries:   3,
		proxyClients: map[string]requestClients{},
	}
}

// PreloadPow 保留兼容接口，纯 Go 实现无需预加载。
func (c *Client) PreloadPow(_ context.Context) error {
	return nil
}
