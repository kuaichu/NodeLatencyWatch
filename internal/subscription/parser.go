package subscription

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"node-latency-watch/internal/model"
)

type clashFile struct {
	Proxies []map[string]any `yaml:"proxies"`
}

func LoadProvider(provider model.Provider) ([]model.ProxyNode, error) {
	return loadProvider(provider, 0)
}

func ReadProviderContent(provider model.Provider, preferRemote bool) ([]byte, string, error) {
	if !preferRemote && strings.TrimSpace(provider.SubscriptionFile) != "" {
		data, err := os.ReadFile(provider.SubscriptionFile)
		return data, provider.SubscriptionFile, err
	}
	if strings.TrimSpace(provider.SubscriptionURL) == "" {
		return nil, "", fmt.Errorf("provider has no subscription_url")
	}
	return fetchSubscriptionContent(provider.SubscriptionURL, 0)
}

func loadProvider(provider model.Provider, redirects int) ([]model.ProxyNode, error) {
	if redirects > 3 {
		return nil, fmt.Errorf("subscription indirection depth exceeded")
	}
	var data []byte
	var err error
	if strings.TrimSpace(provider.SubscriptionFile) != "" {
		data, err = os.ReadFile(provider.SubscriptionFile)
	} else {
		data, err = fetch(provider.SubscriptionURL)
	}
	if err != nil {
		return nil, err
	}
	nodes, err := Parse(provider, data)
	if err != nil {
		if nextURL := extractSubscriptionURL(string(data)); nextURL != "" && nextURL != provider.SubscriptionURL {
			nextProvider := provider
			nextProvider.SubscriptionURL = nextURL
			nextProvider.SubscriptionFile = ""
			return loadProvider(nextProvider, redirects+1)
		}
		return nil, err
	}
	return nodes, nil
}

func fetchSubscriptionContent(rawURL string, redirects int) ([]byte, string, error) {
	if redirects > 3 {
		return nil, "", fmt.Errorf("subscription indirection depth exceeded")
	}
	data, err := fetch(rawURL)
	if err != nil {
		return nil, "", err
	}
	if nextURL := extractSubscriptionURL(string(data)); nextURL != "" && nextURL != rawURL {
		return fetchSubscriptionContent(nextURL, redirects+1)
	}
	return data, rawURL, nil
}

func fetch(rawURL string) ([]byte, error) {
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				CurvePreferences: []tls.CurveID{
					tls.X25519,
					tls.CurveP256,
					tls.CurveP384,
					tls.CurveP521,
				},
			},
		},
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Clash.Meta")
	req.Header.Set("Accept", "text/plain, application/yaml, application/json, */*")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

var subscriptionURLPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<input[^>]+value=["'](https?://[^"']+)["'][^>]*(?:id|name)=["'][^"']*sub[^"']*["']`),
	regexp.MustCompile(`(?i)(?:href|value|data-clipboard-text)=["'](https?://[^"']*(?:subscribe|sub)[^"']*)["']`),
	regexp.MustCompile(`https?://[^\s"'<>]+`),
}

func extractSubscriptionURL(html string) string {
	if !strings.Contains(strings.ToLower(html), "<html") {
		return ""
	}
	for _, pattern := range subscriptionURLPatterns {
		matches := pattern.FindAllStringSubmatch(html, -1)
		for _, match := range matches {
			candidate := ""
			if len(match) > 1 && match[1] != "" {
				candidate = match[1]
			} else if len(match) > 0 {
				candidate = match[0]
			}
			candidate = strings.TrimSpace(candidate)
			if candidate == "" || !strings.Contains(strings.ToLower(candidate), "subscribe") {
				continue
			}
			if decoded, err := url.QueryUnescape(candidate); err == nil {
				return strings.TrimSpace(decoded)
			}
			return candidate
		}
	}
	return ""
}

func Parse(provider model.Provider, data []byte) ([]model.ProxyNode, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, fmt.Errorf("subscription is empty")
	}
	if decoded, ok := tryBase64(text); ok {
		text = strings.TrimSpace(string(decoded))
	}
	var nodes []model.ProxyNode
	if strings.Contains(text, "proxies:") {
		if parsed, err := parseClash(provider, []byte(text)); err == nil {
			nodes = append(nodes, parsed...)
		}
	}
	nodes = append(nodes, parseURILines(provider, text)...)
	nodes = dedupeNodes(nodes)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no supported nodes found")
	}
	return nodes, nil
}

func parseClash(provider model.Provider, data []byte) ([]model.ProxyNode, error) {
	var doc clashFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	nodes := make([]model.ProxyNode, 0, len(doc.Proxies))
	for _, item := range doc.Proxies {
		name := stringValue(item, "name")
		protocol := strings.ToLower(stringValue(item, "type"))
		server := stringValue(item, "server")
		port := intValue(item, "port")
		if name == "" || protocol == "" || server == "" || port <= 0 {
			continue
		}
		node := model.ProxyNode{
			ProviderID: provider.ID,
			Provider:   provider.Name,
			Category:   provider.Category,
			Name:       name,
			Protocol:   protocol,
			Server:     server,
			Port:       port,
			SNI:        firstNonEmpty(stringValue(item, "sni"), stringValue(item, "servername"), stringValue(item, "serverName")),
			Host:       stringValue(item, "host"),
			Path:       stringValue(item, "path"),
			Meta:       map[string]string{},
			Outbound:   cloneProxyMapping(item),
		}
		node.Raw = fmt.Sprintf("%s|%s|%s|%d|%s", provider.ID, protocol, server, port, name)
		node.ID = nodeID(node)
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func parseURILines(provider model.Provider, text string) []model.ProxyNode {
	lines := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	nodes := make([]model.ProxyNode, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if decoded, ok := tryBase64(line); ok && strings.Contains(string(decoded), "://") {
			nodes = append(nodes, parseURILines(provider, string(decoded))...)
			continue
		}
		node, ok := parseShareURI(provider, line)
		if ok {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func parseShareURI(provider model.Provider, raw string) (model.ProxyNode, bool) {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "vmess://"):
		return parseVMess(provider, raw)
	case strings.HasPrefix(lower, "ss://"),
		strings.HasPrefix(lower, "trojan://"),
		strings.HasPrefix(lower, "vless://"),
		strings.HasPrefix(lower, "hysteria2://"),
		strings.HasPrefix(lower, "hy2://"):
		return parseURLNode(provider, raw)
	default:
		return model.ProxyNode{}, false
	}
}

func parseVMess(provider model.Provider, raw string) (model.ProxyNode, bool) {
	payload := strings.TrimPrefix(raw, "vmess://")
	decoded, ok := tryBase64(payload)
	if !ok {
		return model.ProxyNode{}, false
	}
	var doc map[string]any
	if err := json.Unmarshal(decoded, &doc); err != nil {
		return model.ProxyNode{}, false
	}
	server := stringValue(doc, "add")
	port := intValue(doc, "port")
	name := firstNonEmpty(stringValue(doc, "ps"), server)
	if server == "" || port <= 0 {
		return model.ProxyNode{}, false
	}
	node := model.ProxyNode{
		ProviderID: provider.ID,
		Provider:   provider.Name,
		Category:   provider.Category,
		Name:       name,
		Protocol:   "vmess",
		Server:     server,
		Port:       port,
		SNI:        firstNonEmpty(stringValue(doc, "sni"), stringValue(doc, "host")),
		Host:       stringValue(doc, "host"),
		Path:       stringValue(doc, "path"),
		Raw:        raw,
		Outbound: map[string]any{
			"name":             name,
			"type":             "vmess",
			"server":           server,
			"port":             port,
			"uuid":             stringValue(doc, "id"),
			"alterId":          intValue(doc, "aid"),
			"cipher":           firstNonEmpty(stringValue(doc, "scy"), "auto"),
			"tls":              strings.EqualFold(stringValue(doc, "tls"), "tls"),
			"servername":       firstNonEmpty(stringValue(doc, "sni"), stringValue(doc, "host")),
			"network":          stringValue(doc, "net"),
			"ws-opts":          map[string]any{"path": stringValue(doc, "path"), "headers": map[string]any{"Host": stringValue(doc, "host")}},
			"skip-cert-verify": true,
		},
	}
	node.ID = nodeID(node)
	return node, true
}

func parseURLNode(provider model.Provider, raw string) (model.ProxyNode, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return model.ProxyNode{}, false
	}
	protocol := strings.ToLower(u.Scheme)
	if protocol == "hy2" {
		protocol = "hysteria2"
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	name, _ := url.QueryUnescape(strings.TrimPrefix(u.Fragment, "#"))
	if name == "" {
		name = host
	}
	if host == "" || port <= 0 {
		if protocol == "ss" {
			host, port = parseSSHostPort(raw)
		}
	}
	if host == "" || port <= 0 {
		return model.ProxyNode{}, false
	}
	query := u.Query()
	node := model.ProxyNode{
		ProviderID: provider.ID,
		Provider:   provider.Name,
		Category:   provider.Category,
		Name:       name,
		Protocol:   protocol,
		Server:     host,
		Port:       port,
		SNI:        firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("servername")),
		Host:       query.Get("host"),
		Path:       query.Get("path"),
		Raw:        raw,
		Outbound:   outboundFromURLNode(protocol, name, host, port, u),
	}
	node.ID = nodeID(node)
	return node, true
}

func parseSSHostPort(raw string) (string, int) {
	body := strings.TrimPrefix(raw, "ss://")
	if i := strings.Index(body, "#"); i >= 0 {
		body = body[:i]
	}
	if decoded, ok := tryBase64(body); ok {
		body = string(decoded)
	}
	if at := strings.LastIndex(body, "@"); at >= 0 {
		body = body[at+1:]
	}
	host, portText, err := net.SplitHostPort(body)
	if err != nil {
		return "", 0
	}
	port, _ := strconv.Atoi(portText)
	return host, port
}

func cloneProxyMapping(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneProxyMapping(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func outboundFromURLNode(protocol, name, host string, port int, u *url.URL) map[string]any {
	query := u.Query()
	out := map[string]any{
		"name":             name,
		"type":             protocol,
		"server":           host,
		"port":             port,
		"skip-cert-verify": true,
	}
	if user := u.User.Username(); user != "" {
		switch protocol {
		case "vless", "vmess":
			out["uuid"] = user
		case "trojan", "hysteria2":
			out["password"] = user
		}
	}
	if password, ok := u.User.Password(); ok && password != "" {
		out["password"] = password
	}
	if security := firstNonEmpty(query.Get("security"), query.Get("tls")); security != "" {
		out["tls"] = security == "tls" || security == "reality" || security == "true"
		if security == "reality" {
			out["reality-opts"] = map[string]any{
				"public-key": firstNonEmpty(query.Get("pbk"), query.Get("public-key")),
				"short-id":   firstNonEmpty(query.Get("sid"), query.Get("short-id")),
			}
		}
	}
	if sni := firstNonEmpty(query.Get("sni"), query.Get("peer"), query.Get("servername")); sni != "" {
		out["servername"] = sni
	}
	if flow := query.Get("flow"); flow != "" {
		out["flow"] = flow
	}
	if fp := firstNonEmpty(query.Get("fp"), query.Get("client-fingerprint")); fp != "" {
		out["client-fingerprint"] = fp
	}
	if network := firstNonEmpty(query.Get("type"), query.Get("network")); network != "" && network != "tcp" {
		out["network"] = network
	}
	if path := query.Get("path"); path != "" {
		hostHeader := query.Get("host")
		out["ws-opts"] = map[string]any{"path": path, "headers": map[string]any{"Host": hostHeader}}
	}
	return out
}

func nodeID(node model.ProxyNode) string {
	sum := sha256.Sum256([]byte(firstNonEmpty(node.Raw, fmt.Sprintf("%s|%s|%s|%d|%s", node.ProviderID, node.Protocol, node.Server, node.Port, node.Name))))
	return hex.EncodeToString(sum[:])[:20]
}

func dedupeNodes(nodes []model.ProxyNode) []model.ProxyNode {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]model.ProxyNode, 0, len(nodes))
	for _, node := range nodes {
		if node.ID == "" || node.Server == "" || node.Port <= 0 {
			continue
		}
		if _, ok := seen[node.ID]; ok {
			continue
		}
		seen[node.ID] = struct{}{}
		out = append(out, node)
	}
	return out
}

func tryBase64(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "=")
	encodings := []*base64.Encoding{
		base64.StdEncoding.WithPadding(base64.NoPadding),
		base64.URLEncoding.WithPadding(base64.NoPadding),
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(value)
		if err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return nil, false
}

func stringValue(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func intValue(m map[string]any, key string) int {
	value, ok := m[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(typed))
		return n
	default:
		n, _ := strconv.Atoi(fmt.Sprint(typed))
		return n
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
