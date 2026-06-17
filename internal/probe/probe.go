package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"node-latency-watch/internal/model"
)

type attemptResult struct {
	dnsMs      float64
	tcpMs      float64
	tlsMs      float64
	resolvedIP string
	err        error
}

func Run(nodes []model.ProxyNode, cfg model.ProbeConfig) []model.NodeResult {
	if cfg.Attempts <= 0 {
		cfg.Attempts = 1
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 5
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 16
	}
	results := make([]model.NodeResult, len(nodes))
	sem := make(chan struct{}, cfg.MaxConcurrency)
	var wg sync.WaitGroup
	wg.Add(len(nodes))
	for i, node := range nodes {
		go func(idx int, n model.ProxyNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = ProbeNode(n, cfg)
		}(i, node)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].Success != results[j].Success {
			return results[i].Success
		}
		return results[i].TCPMs < results[j].TCPMs
	})
	return results
}

func ProbeNode(node model.ProxyNode, cfg model.ProbeConfig) model.NodeResult {
	result := model.NodeResult{
		NodeID:   node.ID,
		Attempts: cfg.Attempts,
	}
	var successes int
	var dnsValues, tcpValues, tlsValues []float64
	var resolvedIP string
	var lastErr error
	for i := 0; i < cfg.Attempts; i++ {
		attempt := probeOnce(node, cfg)
		if attempt.err != nil {
			lastErr = attempt.err
			continue
		}
		successes++
		dnsValues = append(dnsValues, attempt.dnsMs)
		tcpValues = append(tcpValues, attempt.tcpMs)
		if attempt.tlsMs > 0 {
			tlsValues = append(tlsValues, attempt.tlsMs)
		}
		if resolvedIP == "" {
			resolvedIP = attempt.resolvedIP
		}
	}
	result.Successes = successes
	if cfg.Attempts > 0 {
		result.LossRate = float64(cfg.Attempts-successes) / float64(cfg.Attempts) * 100
	}
	result.Success = successes > 0
	result.DNSMs = average(dnsValues)
	result.TCPMs = average(tcpValues)
	result.TLSMs = average(tlsValues)
	result.ResolvedIP = resolvedIP
	if !result.Success && lastErr != nil {
		result.Error = lastErr.Error()
	}
	return result
}

func probeOnce(node model.ProxyNode, cfg model.ProbeConfig) attemptResult {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	dialer := &net.Dialer{Timeout: timeout}
	host := strings.TrimSpace(node.Server)
	if host == "" || node.Port <= 0 {
		return attemptResult{err: fmt.Errorf("node target is empty")}
	}

	startDNS := time.Now()
	resolved := host
	if net.ParseIP(host) == nil {
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return attemptResult{err: fmt.Errorf("dns lookup %s: %w", host, err)}
		}
		if len(ips) == 0 {
			return attemptResult{err: fmt.Errorf("dns lookup %s: no records", host)}
		}
		resolved = ips[0].IP.String()
	}
	dnsMs := float64(time.Since(startDNS).Microseconds()) / 1000.0

	address := net.JoinHostPort(host, fmt.Sprintf("%d", node.Port))
	startTCP := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return attemptResult{dnsMs: dnsMs, resolvedIP: resolved, err: fmt.Errorf("tcp connect %s: %w", address, err)}
	}
	tcpMs := float64(time.Since(startTCP).Microseconds()) / 1000.0

	tlsMs := 0.0
	if shouldProbeTLS(node, cfg.TLSMode) {
		serverName := firstNonEmpty(node.SNI, node.Host, node.Server)
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true,
		})
		startTLS := time.Now()
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return attemptResult{dnsMs: dnsMs, tcpMs: tcpMs, resolvedIP: resolved, err: fmt.Errorf("tls handshake: %w", err)}
		}
		tlsMs = float64(time.Since(startTLS).Microseconds()) / 1000.0
		tlsConn.Close()
	} else {
		conn.Close()
	}
	return attemptResult{dnsMs: dnsMs, tcpMs: tcpMs, tlsMs: tlsMs, resolvedIP: resolved}
}

func shouldProbeTLS(node model.ProxyNode, mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always":
		return true
	case "never":
		return false
	}
	protocol := strings.ToLower(node.Protocol)
	return node.Port == 443 || protocol == "trojan" || protocol == "vless" || protocol == "vmess" || protocol == "hysteria2" || protocol == "anytls"
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
