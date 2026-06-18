package probe

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	mihomoAdapter "github.com/metacubex/mihomo/adapter"
	mihomoUtils "github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	"node-latency-watch/internal/model"
)

const defaultHTTP204TestURL = "http://www.gstatic.com/generate_204"

var expectedHTTP204, _ = mihomoUtils.NewUnsignedRanges[uint16]("204")

func probeProxyTCPOnce(node model.ProxyNode, cfg model.ProbeConfig) (float64, error) {
	if !hasProxyOutbound(node) {
		return 0, fmt.Errorf("node has no proxy outbound")
	}
	timeout := probeTimeout(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	proxy, err := parseProxy(node)
	if err != nil {
		return 0, err
	}
	defer proxy.Close()

	testURL := probeTestURL(cfg)
	metadata, err := http204Metadata(testURL)
	if err != nil {
		return 0, err
	}

	start := time.Now()
	conn, err := proxy.DialContext(ctx, &metadata)
	if err != nil {
		return 0, fmt.Errorf("proxy tcp %s: %w", metadata.RemoteAddress(), err)
	}
	defer conn.Close()

	return float64(time.Since(start).Microseconds()) / 1000.0, nil
}

func probeHTTP204Once(node model.ProxyNode, cfg model.ProbeConfig) (float64, error) {
	if !hasProxyOutbound(node) {
		return 0, fmt.Errorf("node has no proxy outbound")
	}
	timeout := probeTimeout(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	proxy, err := parseProxy(node)
	if err != nil {
		return 0, err
	}
	defer proxy.Close()

	testURL := probeTestURL(cfg)
	delay, err := proxy.URLTest(ctx, testURL, expectedHTTP204)
	if err != nil {
		return 0, fmt.Errorf("http 204: %w", err)
	}
	return float64(delay), nil
}

func probeTestURL(cfg model.ProbeConfig) string {
	if cfg.TestURL != "" {
		return cfg.TestURL
	}
	return defaultHTTP204TestURL
}

func probeTimeout(cfg model.ProbeConfig) time.Duration {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return timeout
}

func parseProxy(node model.ProxyNode) (C.Proxy, error) {
	proxy, err := mihomoAdapter.ParseProxy(cloneOutbound(node.Outbound))
	if err != nil {
		return nil, fmt.Errorf("parse proxy: %w", err)
	}
	return proxy, nil
}

func http204Metadata(testURL string) (C.Metadata, error) {
	u, err := url.Parse(testURL)
	if err != nil {
		return C.Metadata{}, err
	}

	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return C.Metadata{}, fmt.Errorf("%s scheme not supported", testURL)
		}
	}

	var metadata C.Metadata
	metadata.NetWork = C.TCP
	if err := metadata.SetRemoteAddress(net.JoinHostPort(u.Hostname(), port)); err != nil {
		return C.Metadata{}, err
	}
	return metadata, nil
}

func cloneOutbound(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneOutboundValue(v)
	}
	return out
}

func cloneOutboundValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneOutbound(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneOutboundValue(item)
		}
		return out
	default:
		return typed
	}
}
