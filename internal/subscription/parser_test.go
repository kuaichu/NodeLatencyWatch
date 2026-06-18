package subscription

import (
	"strings"
	"testing"

	"node-latency-watch/internal/model"
)

func TestExtractSubscriptionURLFromInfoPage(t *testing.T) {
	html := `<html><body><input type="text" value="https://example.com:30010/subscribe/token" readonly id="sub_url"></body></html>`
	got := extractSubscriptionURL(html)
	want := "https://example.com:30010/subscribe/token"
	if got != want {
		t.Fatalf("extractSubscriptionURL() = %q, want %q", got, want)
	}
}

func TestExtractSubscriptionURLFallbackScan(t *testing.T) {
	html := `<html><body><script>const u = "https://example.com/subscribe/token";</script></body></html>`
	got := extractSubscriptionURL(html)
	want := "https://example.com/subscribe/token"
	if got != want {
		t.Fatalf("extractSubscriptionURL() = %q, want %q", got, want)
	}
}

func TestParseFiltersSubscriptionInfoNodes(t *testing.T) {
	provider := model.Provider{ID: "p1", Name: "机场"}
	text := strings.Join([]string{
		"vless://uuid@example.com:443?security=tls#HK01",
		"vless://uuid@127.0.0.1:443?security=tls#剩余流量：1.65 TB",
		"vless://uuid@127.0.0.1:443?security=tls#套餐到期：长期有效",
		"trojan://pass@example.org:443#官网地址",
	}, "\n")
	nodes, err := Parse(provider, []byte(text))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Parse() returned %d nodes, want 1: %#v", len(nodes), nodes)
	}
	if nodes[0].Name != "HK01" {
		t.Fatalf("remaining node = %q, want HK01", nodes[0].Name)
	}
}
