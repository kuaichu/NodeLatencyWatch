package probe

import (
	"math"
	"testing"

	"node-latency-watch/internal/model"
)

func TestProbeProxyNodeUsesProxyTCPForRTT(t *testing.T) {
	oldTCP := probeProxyTCP
	oldHTTP := probeProxyHTTP204
	t.Cleanup(func() {
		probeProxyTCP = oldTCP
		probeProxyHTTP204 = oldHTTP
	})

	var tcpCalls int
	var httpCalls int
	probeProxyTCP = func(node model.ProxyNode, cfg model.ProbeConfig) (float64, error) {
		tcpCalls++
		return float64(40 + tcpCalls), nil
	}
	probeProxyHTTP204 = func(node model.ProxyNode, cfg model.ProbeConfig) (float64, error) {
		httpCalls++
		return float64(100 + httpCalls), nil
	}

	result := probeProxyNode(model.ProxyNode{
		ID:       "relay-node",
		Server:   "127.0.0.1",
		Port:     1,
		Outbound: map[string]any{"type": "stub"},
	}, model.ProbeConfig{Attempts: 2})

	if tcpCalls != 2 {
		t.Fatalf("expected 2 proxy tcp probes, got %d", tcpCalls)
	}
	if httpCalls != 2 {
		t.Fatalf("expected 2 http probes, got %d", httpCalls)
	}
	assertFloat(t, "TCPMs", result.TCPMs, 41.5)
	assertFloat(t, "MaxRTTMs", result.MaxRTTMs, 42)
	assertFloat(t, "RTTStdDevMs", result.RTTStdDevMs, 0.5)
	assertFloat(t, "HTTPMs", result.HTTPMs, 101.5)
	if result.DNSMs != 0 {
		t.Fatalf("proxy mode should not fill direct-entry DNSMs, got %.2f", result.DNSMs)
	}
	if result.ResolvedIP != "" {
		t.Fatalf("proxy mode should not fill direct-entry resolved IP, got %q", result.ResolvedIP)
	}
}

func TestProbeTestURLDefaultsToHTTP204(t *testing.T) {
	got := probeTestURL(model.ProbeConfig{})
	if got != "http://www.gstatic.com/generate_204" {
		t.Fatalf("probeTestURL() = %q", got)
	}
}

func TestHTTP204MetadataUsesConfiguredURLPort(t *testing.T) {
	metadata, err := http204Metadata("http://cp.cloudflare.com/generate_204")
	if err != nil {
		t.Fatalf("http204Metadata() error = %v", err)
	}
	if got := metadata.RemoteAddress(); got != "cp.cloudflare.com:80" {
		t.Fatalf("RemoteAddress() = %q, want cp.cloudflare.com:80", got)
	}

	metadata, err = http204Metadata("https://www.gstatic.com/generate_204")
	if err != nil {
		t.Fatalf("http204Metadata() error = %v", err)
	}
	if got := metadata.RemoteAddress(); got != "www.gstatic.com:443" {
		t.Fatalf("RemoteAddress() = %q, want www.gstatic.com:443", got)
	}
}

func assertFloat(t *testing.T, name string, got float64, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.001 {
		t.Fatalf("%s = %.3f, want %.3f", name, got, want)
	}
}
