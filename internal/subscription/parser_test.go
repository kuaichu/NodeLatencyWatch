package subscription

import "testing"

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
