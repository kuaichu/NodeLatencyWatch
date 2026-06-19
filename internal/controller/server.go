package controller

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"node-latency-watch/internal/config"
	"node-latency-watch/internal/model"
	"node-latency-watch/internal/subscription"
)

//go:embed dashboard.html
var dashboardFS embed.FS

var detectControllerURLs = detectControllerInterfaceURLs

type Server struct {
	configPath string
	cfg        *config.Config
	cfgMu      sync.RWMutex
	store      *Store

	nodesMu             sync.RWMutex
	nodes               []model.ProxyNode
	nodesByID           map[string]model.ProxyNode
	subscriptionVersion string
	lastRefresh         time.Time
	lastRefreshError    string
}

type adminConfigResponse struct {
	WebPort             int               `json:"webPort"`
	StateDir            string            `json:"stateDir"`
	AgentTokenSet       bool              `json:"agentTokenSet"`
	AgentReportTTL      int               `json:"agentReportTtl"`
	Agents              []model.AgentPeer `json:"agents"`
	Providers           []model.Provider  `json:"providers"`
	Probe               model.ProbeConfig `json:"probe"`
	LastRefresh         time.Time         `json:"lastRefresh"`
	LastRefreshError    string            `json:"lastRefreshError"`
	SubscriptionVersion string            `json:"subscriptionVersion"`
}

type adminConfigRequest struct {
	AgentToken     *string           `json:"agentToken"`
	AgentReportTTL *int              `json:"agentReportTtl"`
	Agents         []model.AgentPeer `json:"agents"`
	Providers      []model.Provider  `json:"providers"`
	Probe          model.ProbeConfig `json:"probe"`
}

type agentAdminStatus struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ProbeSource  string    `json:"probeSource"`
	Carrier      string    `json:"carrier"`
	CarrierLabel string    `json:"carrierLabel"`
	Configured   bool      `json:"configured"`
	LastSeen     time.Time `json:"lastSeen,omitempty"`
	AgeSeconds   int       `json:"ageSeconds"`
	ResultCount  int       `json:"resultCount"`
	Status       string    `json:"status"`
}

type installCommandResponse struct {
	Command  string `json:"command"`
	TokenSet bool   `json:"tokenSet"`
}

type providerSubscriptionResponse struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Content    string `json:"content"`
	NodeCount  int    `json:"nodeCount"`
	ParseError string `json:"parseError,omitempty"`
}

type providerSubscriptionRequest struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

func New(configPath string, cfg *config.Config) (*Server, error) {
	store, err := OpenStore(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	return &Server{
		configPath: configPath,
		cfg:        cfg,
		store:      store,
		nodesByID:  map[string]model.ProxyNode{},
	}, nil
}

func (s *Server) Close() {
	_ = s.store.Close()
}

func (s *Server) configSnapshot() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	cp := *s.cfg
	cp.Agents = append([]model.AgentPeer(nil), s.cfg.Agents...)
	cp.Providers = append([]model.Provider(nil), s.cfg.Providers...)
	return &cp
}

func (s *Server) replaceConfig(next *config.Config) {
	s.cfgMu.Lock()
	s.cfg = next
	s.cfgMu.Unlock()
}

func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/nodes", s.handleNodes)
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/samples", s.handleSamples)
	mux.HandleFunc("/api/providers/refresh", s.handleProviderRefresh)
	mux.HandleFunc("/api/admin/config", s.handleAdminConfig)
	mux.HandleFunc("/api/admin/agents", s.handleAdminAgents)
	mux.HandleFunc("/api/admin/install-command", s.handleInstallCommand)
	mux.HandleFunc("/api/admin/providers", s.handleAdminProviders)
	mux.HandleFunc("/api/admin/provider-subscription", s.handleProviderSubscription)
	mux.HandleFunc("/api/agent/download/", s.handleAgentDownload)
	mux.HandleFunc("/api/agent/jobs", s.handleAgentJobs)
	mux.HandleFunc("/api/agent/reports", s.handleAgentReports)
	mux.HandleFunc("/install.sh", s.handleInstallScript)

	addr := fmt.Sprintf(":%d", s.cfg.WebPort)
	log.Printf("[web] dashboard at http://0.0.0.0%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("[web] server stopped: %v", err)
	}
}

func (s *Server) handleProviderSubscription(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		preferRemote := r.URL.Query().Get("remote") == "1"
		provider, ok := s.providerByID(id)
		if !ok {
			writeJSON(w, map[string]string{"error": "provider not found"})
			return
		}
		data, source, err := subscription.ReadProviderContent(provider, preferRemote)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		resp := providerSubscriptionResponse{ID: provider.ID, Source: source, Content: string(data)}
		if nodes, err := subscription.Parse(provider, data); err == nil {
			resp.NodeCount = len(nodes)
		} else {
			resp.ParseError = err.Error()
		}
		writeJSON(w, resp)
	case "POST":
		var req providerSubscriptionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		if err := s.saveProviderSubscription(req); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		provider, _ := s.providerByID(req.ID)
		nodes, parseErr := subscription.Parse(provider, []byte(req.Content))
		_, refreshErr := s.RefreshProvider(req.ID)
		resp := map[string]any{"ok": true, "nodeCount": len(nodes), "config": s.adminConfig()}
		if parseErr != nil {
			resp["parseError"] = parseErr.Error()
		}
		if refreshErr != nil {
			resp["refreshError"] = refreshErr.Error()
		}
		writeJSON(w, resp)
	case "DELETE":
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if err := s.clearProviderSubscriptionFile(id); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		resp := map[string]any{"ok": true, "config": s.adminConfig()}
		if _, err := s.RefreshProvider(id); err != nil {
			resp["refreshError"] = err.Error()
		}
		writeJSON(w, resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleInstallCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.configSnapshot()
	controllerURLs := s.controllerAccessURLs(r)
	controllerURL := controllerURLs[0]
	fallbackControllerURL := ""
	if len(controllerURLs) > 1 {
		fallbackControllerURL = controllerURLs[1]
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	dynamicID := id == "" || isHostnamePlaceholder(id)
	if dynamicID {
		id = "agent-$(hostname -s)"
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	dynamicName := dynamicID && (name == "" || name == "新 Agent" || isHostnamePlaceholder(name))
	if dynamicName {
		name = "$(hostname -s)"
	} else if name == "" {
		name = id
	}
	probeSource := strings.TrimSpace(r.URL.Query().Get("probe_source"))
	dynamicProbeSource := false
	if probeSource == "" {
		probeSource = name
		dynamicProbeSource = dynamicName
	} else if isHostnamePlaceholder(probeSource) {
		probeSource = "$(hostname -s)"
		dynamicProbeSource = true
	}
	carrier := model.NormalizeCarrier(r.URL.Query().Get("carrier"))
	token := strings.TrimSpace(cfg.Agent.Token)
	installFetch := "curl -fsSL " + shellQuote(controllerURL+"/install.sh")
	if fallbackControllerURL != "" {
		installFetch = "(" + installFetch + " || curl -fsSL " + shellQuote(fallbackControllerURL+"/install.sh") + ")"
	}
	parts := []string{
		installFetch,
		"|",
		"sudo bash -s --",
		"--controller " + shellQuote(controllerURL),
	}
	if fallbackControllerURL != "" {
		parts = append(parts, "--fallback-controller "+shellQuote(fallbackControllerURL))
	}
	parts = append(parts,
		"--id "+shellValue(id, dynamicID),
		"--name "+shellValue(name, dynamicName),
		"--probe-source "+shellValue(probeSource, dynamicProbeSource),
		"--carrier "+shellQuote(carrier),
	)
	if token != "" {
		parts = append(parts, "--token "+shellQuote(token))
	} else {
		parts = append(parts, "--token '<填入主控通信Token>'")
	}
	writeJSON(w, installCommandResponse{Command: strings.Join(parts, " "), TokenSet: token != ""})
}

func (s *Server) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write([]byte(agentInstallScript()))
}

func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target := strings.TrimPrefix(r.URL.Path, "/api/agent/download/")
	target = strings.Trim(strings.ToLower(target), "/")
	if target == "" || strings.Contains(target, "..") || strings.ContainsAny(target, `\/`) {
		http.Error(w, "invalid target", http.StatusBadRequest)
		return
	}
	name := "node-latency-agent-" + target
	if strings.HasPrefix(target, "windows-") {
		name += ".exe"
	}
	path := filepath.Join("bin", name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "agent binary not found: "+name, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) StartProviderRefreshLoop() {
	cfg := s.configSnapshot()
	interval := time.Duration(cfg.Probe.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := s.RefreshProviders(); err != nil {
				log.Printf("[providers] refresh failed: %v", err)
			}
		}
	}()
}

func (s *Server) RefreshProviders() error {
	var all []model.ProxyNode
	var failures []string
	cfg := s.configSnapshot()
	for _, provider := range cfg.Providers {
		nodes, err := subscription.LoadProvider(provider)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", provider.ID, err))
			continue
		}
		all = append(all, nodes...)
		log.Printf("[providers] %s loaded %d nodes", provider.ID, len(nodes))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ProviderID != all[j].ProviderID {
			return all[i].ProviderID < all[j].ProviderID
		}
		return all[i].Name < all[j].Name
	})
	byID := make(map[string]model.ProxyNode, len(all))
	for _, node := range all {
		byID[node.ID] = node
	}
	s.nodesMu.Lock()
	if len(all) == 0 && len(failures) > 0 && len(s.nodes) > 0 {
		s.lastRefresh = time.Now()
		s.lastRefreshError = strings.Join(failures, "; ")
		s.nodesMu.Unlock()
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	s.nodes = all
	s.nodesByID = byID
	s.lastRefresh = time.Now()
	s.lastRefreshError = strings.Join(failures, "; ")
	s.subscriptionVersion = fmt.Sprintf("%d:%d", s.lastRefresh.Unix(), len(all))
	s.nodesMu.Unlock()
	if len(all) == 0 && len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func (s *Server) RefreshProvider(id string) (int, error) {
	id = strings.TrimSpace(id)
	provider, ok := s.providerByID(id)
	if !ok {
		return 0, fmt.Errorf("provider not found")
	}
	nodes, err := subscription.LoadProvider(provider)
	if err != nil {
		return 0, err
	}
	s.nodesMu.Lock()
	all := make([]model.ProxyNode, 0, len(s.nodes)+len(nodes))
	for _, node := range s.nodes {
		if node.ProviderID != provider.ID {
			all = append(all, node)
		}
	}
	all = append(all, nodes...)
	sort.Slice(all, func(i, j int) bool {
		if all[i].ProviderID != all[j].ProviderID {
			return all[i].ProviderID < all[j].ProviderID
		}
		return all[i].Name < all[j].Name
	})
	byID := make(map[string]model.ProxyNode, len(all))
	for _, node := range all {
		byID[node.ID] = node
	}
	s.nodes = all
	s.nodesByID = byID
	s.lastRefresh = time.Now()
	s.lastRefreshError = ""
	s.subscriptionVersion = fmt.Sprintf("%d:%d", s.lastRefresh.Unix(), len(all))
	s.nodesMu.Unlock()
	log.Printf("[providers] %s refreshed %d nodes", provider.ID, len(nodes))
	return len(nodes), nil
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := dashboardFS.ReadFile("dashboard.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setNoStore(w)
	_, _ = w.Write(data)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	reports, _ := s.store.AgentReports(time.Duration(cfg.Agent.ReportTTLSeconds) * time.Second)
	agentIDs := configuredAgentSet(cfg)
	s.nodesMu.RLock()
	nodesCount := len(s.nodes)
	lastRefresh := s.lastRefresh
	lastRefreshError := s.lastRefreshError
	version := s.subscriptionVersion
	s.nodesMu.RUnlock()
	writeJSON(w, map[string]any{
		"nodeCount":           nodesCount,
		"providerCount":       len(cfg.Providers),
		"agentCount":          countReportsForAgents(reports, agentIDs),
		"configuredAgents":    cfg.Agents,
		"lastRefresh":         lastRefresh,
		"lastRefreshError":    lastRefreshError,
		"subscriptionVersion": version,
		"probe":               cfg.Probe,
	})
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	s.nodesMu.RLock()
	nodes := append([]model.ProxyNode(nil), s.nodes...)
	s.nodesMu.RUnlock()
	writeJSON(w, publicNodes(nodes))
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	agentCarriers := configuredAgentCarriers(cfg)
	s.nodesMu.RLock()
	nodes := append([]model.ProxyNode(nil), s.nodes...)
	s.nodesMu.RUnlock()
	nodesIndex := indexNodesByID(nodes)
	samples, err := s.store.LatestSamples(5000)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	samples = filterSamplesForAgents(samples, agentCarriers)
	latestByNodeCarrier := make(map[string]map[string]model.NodeSample)
	stats := make(map[string][]model.NodeSample)
	for _, sample := range samples {
		node, ok := nodesIndex[sample.NodeID]
		if !ok || !sampleMatchesCurrentProbeMode(sample, node) {
			continue
		}
		stats[sample.NodeID] = append(stats[sample.NodeID], sample)
		if latestByNodeCarrier[sample.NodeID] == nil {
			latestByNodeCarrier[sample.NodeID] = map[string]model.NodeSample{}
		}
		key := sample.Carrier
		if key == "" {
			key = sample.AgentID
		}
		current, ok := latestByNodeCarrier[sample.NodeID][key]
		if !ok || sample.Time.After(current.Time) {
			latestByNodeCarrier[sample.NodeID][key] = sample
		}
	}
	overview := make([]model.NodeOverview, 0, len(nodes))
	for _, node := range nodes {
		item := model.NodeOverview{Node: publicNode(node)}
		for _, sample := range latestByNodeCarrier[node.ID] {
			item.Latest = append(item.Latest, sample)
		}
		sort.Slice(item.Latest, func(i, j int) bool { return item.Latest[i].Carrier < item.Latest[j].Carrier })
		var okCount int
		for _, sample := range stats[node.ID] {
			if sample.Success {
				okCount++
				if item.BestTCPMs == 0 || sample.TCPMs < item.BestTCPMs {
					item.BestTCPMs = sample.TCPMs
				}
				if sample.TCPMs > item.WorstTCPMs {
					item.WorstTCPMs = sample.TCPMs
				}
			}
			if sample.Time.After(item.LastSeen) {
				item.LastSeen = sample.Time
			}
		}
		if len(stats[node.ID]) > 0 {
			item.SuccessRate = float64(okCount) / float64(len(stats[node.ID])) * 100
		}
		overview = append(overview, item)
	}
	writeJSON(w, overview)
}

func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	agentCarriers := configuredAgentCarriers(cfg)
	limit := 2000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 10000 {
		limit = 10000
	}
	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			hours = parsed
		}
	}
	if hours > 168 {
		hours = 168
	}
	carrierFilter := model.NormalizeCarrier(r.URL.Query().Get("carrier"))
	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		samples, err := s.store.LatestSamples(1000)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		samples = filterSamplesForAgents(samples, agentCarriers)
		samples = s.filterSamplesByCurrentProbeMode(samples)
		if carrierFilter != "unknown" {
			samples = filterSamplesByCarrier(samples, carrierFilter)
		}
		writeJSON(w, samples)
		return
	}
	samples, err := s.store.SamplesForNode(nodeID, time.Now().Add(-time.Duration(hours)*time.Hour), limit)
	if err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	samples = filterSamplesForAgents(samples, agentCarriers)
	if node, ok := s.currentNodeByID(nodeID); ok {
		samples = filterSamplesByProbeMode(samples, node)
	}
	if carrierFilter != "unknown" {
		samples = filterSamplesByCarrier(samples, carrierFilter)
	}
	writeJSON(w, samples)
}

func (s *Server) handleProviderRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id != "" {
		count, err := s.RefreshProvider(id)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "nodeCount": count})
		return
	}
	if err := s.RefreshProviders(); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		writeJSON(w, s.adminConfig())
	case "POST":
		var req adminConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		current := s.configSnapshot()
		next := *current
		next.Agents = append([]model.AgentPeer(nil), req.Agents...)
		next.Providers = append([]model.Provider(nil), req.Providers...)
		next.Probe = req.Probe
		if req.AgentReportTTL != nil {
			next.Agent.ReportTTLSeconds = *req.AgentReportTTL
		}
		if req.AgentToken != nil && strings.TrimSpace(*req.AgentToken) != "" {
			next.Agent.Token = strings.TrimSpace(*req.AgentToken)
		}
		if err := config.Save(s.configPath, &next); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		loaded, err := config.Load(s.configPath)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		s.replaceConfig(loaded)
		if r.URL.Query().Get("refresh") == "0" {
			writeJSON(w, map[string]any{"ok": true, "config": s.adminConfig()})
			return
		}
		if err := s.RefreshProviders(); err != nil {
			writeJSON(w, map[string]any{"ok": true, "refreshError": err.Error(), "config": s.adminConfig()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "config": s.adminConfig()})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.agentStatuses(r.URL.Query().Get("include_unconfigured") == "1"))
}

func (s *Server) handleAdminProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg := s.configSnapshot()
	type providerStatus struct {
		model.Provider
		NodeCount int `json:"nodeCount"`
	}
	counts := make(map[string]int)
	s.nodesMu.RLock()
	for _, node := range s.nodes {
		counts[node.ProviderID]++
	}
	s.nodesMu.RUnlock()
	out := make([]providerStatus, 0, len(cfg.Providers))
	for _, provider := range cfg.Providers {
		out = append(out, providerStatus{Provider: provider, NodeCount: counts[provider.ID]})
	}
	writeJSON(w, out)
}

func (s *Server) adminConfig() adminConfigResponse {
	cfg := s.configSnapshot()
	s.nodesMu.RLock()
	lastRefresh := s.lastRefresh
	lastRefreshError := s.lastRefreshError
	version := s.subscriptionVersion
	s.nodesMu.RUnlock()
	return adminConfigResponse{
		WebPort:             cfg.WebPort,
		StateDir:            cfg.StateDir,
		AgentTokenSet:       strings.TrimSpace(cfg.Agent.Token) != "",
		AgentReportTTL:      cfg.Agent.ReportTTLSeconds,
		Agents:              append([]model.AgentPeer(nil), cfg.Agents...),
		Providers:           append([]model.Provider(nil), cfg.Providers...),
		Probe:               cfg.Probe,
		LastRefresh:         lastRefresh,
		LastRefreshError:    lastRefreshError,
		SubscriptionVersion: version,
	}
}

func (s *Server) agentStatuses(includeUnconfigured bool) []agentAdminStatus {
	cfg := s.configSnapshot()
	ttl := time.Duration(cfg.Agent.ReportTTLSeconds) * time.Second
	reports, _ := s.store.AgentReports(0)
	byID := make(map[string]model.AgentReport, len(reports))
	for _, report := range reports {
		if invalidAgentIdentity(report.AgentID) {
			_ = s.store.DeleteAgentReport(report.AgentID)
			continue
		}
		current, ok := byID[report.AgentID]
		if !ok || report.FinishedAt.After(current.FinishedAt) {
			byID[report.AgentID] = report
		}
	}
	seen := make(map[string]struct{})
	out := make([]agentAdminStatus, 0, len(cfg.Agents)+len(byID))
	for _, peer := range cfg.Agents {
		status := agentAdminStatus{
			ID:           peer.ID,
			Name:         peer.Name,
			ProbeSource:  peer.ProbeSource,
			Carrier:      peer.Carrier,
			CarrierLabel: model.CarrierLabel(peer.Carrier),
			Configured:   true,
			Status:       "offline",
		}
		if report, ok := byID[peer.ID]; ok {
			status.LastSeen = report.FinishedAt
			status.AgeSeconds = int(time.Since(report.FinishedAt).Seconds())
			status.ResultCount = len(report.Results)
			if ttl <= 0 || time.Since(report.FinishedAt) <= ttl {
				status.Status = "online"
			} else {
				status.Status = "stale"
			}
		}
		seen[strings.ToLower(peer.ID)] = struct{}{}
		out = append(out, status)
	}
	if !includeUnconfigured {
		sort.Slice(out, func(i, j int) bool {
			if out[i].Status != out[j].Status {
				return out[i].Status < out[j].Status
			}
			return out[i].ID < out[j].ID
		})
		return out
	}
	for _, report := range byID {
		key := strings.ToLower(report.AgentID)
		if _, ok := seen[key]; ok {
			continue
		}
		status := agentAdminStatus{
			ID:           report.AgentID,
			Name:         firstNonEmpty(report.AgentName, report.AgentID),
			ProbeSource:  report.ProbeSource,
			Carrier:      report.Carrier,
			CarrierLabel: report.CarrierLabel,
			LastSeen:     report.FinishedAt,
			AgeSeconds:   int(time.Since(report.FinishedAt).Seconds()),
			ResultCount:  len(report.Results),
			Status:       "stale",
		}
		if ttl <= 0 || time.Since(report.FinishedAt) <= ttl {
			status.Status = "online"
		}
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return out[i].Status < out[j].Status
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Server) handleAgentJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.agentAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	cfg := s.configSnapshot()
	peer := s.peerForAgent(agentID)
	s.nodesMu.RLock()
	nodes := append([]model.ProxyNode(nil), s.nodes...)
	version := s.subscriptionVersion
	s.nodesMu.RUnlock()
	writeJSON(w, model.JobResponse{
		ServerTime:          time.Now(),
		AgentName:           peer.Name,
		AgentProbeSource:    peer.ProbeSource,
		AgentCarrier:        peer.Carrier,
		AgentCarrierLabel:   model.CarrierLabel(peer.Carrier),
		Probe:               cfg.Probe,
		Nodes:               nodes,
		SubscriptionVersion: version,
	})
}

func (s *Server) handleAgentReports(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		cfg := s.configSnapshot()
		reports, err := s.store.AgentReports(time.Duration(cfg.Agent.ReportTTLSeconds) * time.Second)
		if err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, reports)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.agentAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var report model.AgentReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeJSON(w, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if strings.TrimSpace(report.AgentID) == "" {
		writeJSON(w, map[string]string{"error": "agentId is required"})
		return
	}
	if invalidAgentIdentity(report.AgentID) {
		writeJSON(w, map[string]string{"error": "agentId still contains a hostname placeholder; reinstall the agent with the latest install command"})
		return
	}
	peer := s.peerForAgent(report.AgentID)
	if report.AgentName == "" {
		report.AgentName = peer.Name
	}
	if report.ProbeSource == "" {
		report.ProbeSource = peer.ProbeSource
	}
	report.Carrier = model.NormalizeCarrier(firstNonEmpty(report.Carrier, peer.Carrier))
	report.CarrierLabel = model.CarrierLabel(report.Carrier)
	if report.FinishedAt.IsZero() {
		report.FinishedAt = time.Now()
	}
	if report.StartedAt.IsZero() {
		report.StartedAt = report.FinishedAt
	}
	samples := s.samplesFromReport(report)
	if err := s.store.UpsertAgentReport(report); err != nil {
		log.Printf("[agent] persist report failed: %v", err)
	}
	if err := s.store.InsertSamples(samples); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("[agent] report accepted from %s results=%d samples=%d", report.AgentID, len(report.Results), len(samples))
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) samplesFromReport(report model.AgentReport) []model.NodeSample {
	s.nodesMu.RLock()
	byID := make(map[string]model.ProxyNode, len(s.nodesByID))
	for k, v := range s.nodesByID {
		byID[k] = v
	}
	s.nodesMu.RUnlock()
	samples := make([]model.NodeSample, 0, len(report.Results))
	for _, result := range report.Results {
		node, ok := byID[result.NodeID]
		if !ok {
			continue
		}
		samples = append(samples, model.NodeSample{
			Time:         report.FinishedAt,
			AgentID:      report.AgentID,
			AgentName:    report.AgentName,
			Carrier:      report.Carrier,
			CarrierLabel: report.CarrierLabel,
			ProbeSource:  report.ProbeSource,
			ProviderID:   node.ProviderID,
			Provider:     node.Provider,
			Category:     node.Category,
			NodeID:       node.ID,
			NodeName:     node.Name,
			Protocol:     node.Protocol,
			Server:       node.Server,
			Port:         node.Port,
			DNSMs:        result.DNSMs,
			TCPMs:        result.TCPMs,
			TLSMs:        result.TLSMs,
			MaxRTTMs:     result.MaxRTTMs,
			RTTStdDevMs:  result.RTTStdDevMs,
			HTTPMs:       result.HTTPMs,
			Attempts:     result.Attempts,
			Successes:    result.Successes,
			LossRate:     result.LossRate,
			Success:      result.Success,
			Error:        result.Error,
			ResolvedIP:   result.ResolvedIP,
			ProbeMode:    result.ProbeMode,
		})
	}
	return samples
}

func (s *Server) peerForAgent(agentID string) model.AgentPeer {
	cfg := s.configSnapshot()
	for _, peer := range cfg.Agents {
		if strings.EqualFold(peer.ID, agentID) {
			return peer
		}
	}
	return model.AgentPeer{
		ID:          agentID,
		Name:        firstNonEmpty(agentID, "unknown-agent"),
		ProbeSource: "",
		Carrier:     "unknown",
	}
}

func (s *Server) providerByID(id string) (model.Provider, bool) {
	id = strings.TrimSpace(id)
	cfg := s.configSnapshot()
	for _, provider := range cfg.Providers {
		if strings.EqualFold(provider.ID, id) {
			return provider, true
		}
	}
	return model.Provider{}, false
}

func (s *Server) saveProviderSubscription(req providerSubscriptionRequest) error {
	id := strings.TrimSpace(req.ID)
	content := req.Content
	if id == "" {
		return fmt.Errorf("provider id is required")
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("subscription content is empty")
	}
	current := s.configSnapshot()
	index := -1
	for i, provider := range current.Providers {
		if strings.EqualFold(provider.ID, id) {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("provider not found")
	}
	dir := filepath.Join(current.StateDir, "subscriptions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, current.Providers[index].ID+".txt")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return err
	}
	next := *current
	next.Providers = append([]model.Provider(nil), current.Providers...)
	next.Providers[index].SubscriptionFile = path
	if err := config.Save(s.configPath, &next); err != nil {
		return err
	}
	loaded, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	s.replaceConfig(loaded)
	return nil
}

func (s *Server) clearProviderSubscriptionFile(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("provider id is required")
	}
	current := s.configSnapshot()
	index := -1
	oldPath := ""
	for i, provider := range current.Providers {
		if strings.EqualFold(provider.ID, id) {
			index = i
			oldPath = provider.SubscriptionFile
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("provider not found")
	}
	next := *current
	next.Providers = append([]model.Provider(nil), current.Providers...)
	next.Providers[index].SubscriptionFile = ""
	if err := config.Save(s.configPath, &next); err != nil {
		return err
	}
	if oldPath != "" {
		removePath := oldPath
		if !filepath.IsAbs(removePath) {
			removePath = filepath.Join(filepath.Dir(s.configPath), removePath)
		}
		_ = os.Remove(removePath)
	}
	loaded, err := config.Load(s.configPath)
	if err != nil {
		return err
	}
	s.replaceConfig(loaded)
	return nil
}

func (s *Server) agentAuthorized(r *http.Request) bool {
	cfg := s.configSnapshot()
	token := strings.TrimSpace(cfg.Agent.Token)
	if token == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		header = strings.TrimSpace(header[7:])
	}
	if header == "" {
		header = strings.TrimSpace(r.Header.Get("X-Agent-Token"))
	}
	return header == token
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	setNoStore(w)
	_ = json.NewEncoder(w).Encode(value)
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func configuredAgentSet(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{}, len(cfg.Agents))
	for _, peer := range cfg.Agents {
		id := strings.ToLower(strings.TrimSpace(peer.ID))
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func configuredAgentCarriers(cfg *config.Config) map[string]string {
	out := make(map[string]string, len(cfg.Agents))
	for _, peer := range cfg.Agents {
		id := strings.ToLower(strings.TrimSpace(peer.ID))
		if id != "" {
			out[id] = model.NormalizeCarrier(peer.Carrier)
		}
	}
	return out
}

func countReportsForAgents(reports []model.AgentReport, agentIDs map[string]struct{}) int {
	if len(agentIDs) == 0 {
		return 0
	}
	count := 0
	for _, report := range reports {
		if _, ok := agentIDs[strings.ToLower(strings.TrimSpace(report.AgentID))]; ok {
			count++
		}
	}
	return count
}

func filterSamplesForAgents(samples []model.NodeSample, agentCarriers map[string]string) []model.NodeSample {
	if len(agentCarriers) == 0 || len(samples) == 0 {
		return nil
	}
	out := samples[:0]
	for _, sample := range samples {
		carrier, ok := agentCarriers[strings.ToLower(strings.TrimSpace(sample.AgentID))]
		if !ok {
			continue
		}
		if carrier == "unknown" || carrier == model.NormalizeCarrier(sample.Carrier) {
			out = append(out, sample)
		}
	}
	return out
}

func filterSamplesByCarrier(samples []model.NodeSample, carrier string) []model.NodeSample {
	if len(samples) == 0 {
		return nil
	}
	out := samples[:0]
	for _, sample := range samples {
		if model.NormalizeCarrier(sample.Carrier) == carrier {
			out = append(out, sample)
		}
	}
	return out
}

func (s *Server) currentNodeByID(nodeID string) (model.ProxyNode, bool) {
	s.nodesMu.RLock()
	defer s.nodesMu.RUnlock()
	node, ok := s.nodesByID[nodeID]
	return node, ok
}

func (s *Server) filterSamplesByCurrentProbeMode(samples []model.NodeSample) []model.NodeSample {
	if len(samples) == 0 {
		return nil
	}
	s.nodesMu.RLock()
	nodes := make(map[string]model.ProxyNode, len(s.nodesByID))
	for id, node := range s.nodesByID {
		nodes[id] = node
	}
	s.nodesMu.RUnlock()
	out := samples[:0]
	for _, sample := range samples {
		node, ok := nodes[sample.NodeID]
		if ok && sampleMatchesCurrentProbeMode(sample, node) {
			out = append(out, sample)
		}
	}
	return out
}

func indexNodesByID(nodes []model.ProxyNode) map[string]model.ProxyNode {
	out := make(map[string]model.ProxyNode, len(nodes))
	for _, node := range nodes {
		out[node.ID] = node
	}
	return out
}

func filterSamplesByProbeMode(samples []model.NodeSample, node model.ProxyNode) []model.NodeSample {
	if len(samples) == 0 {
		return nil
	}
	out := samples[:0]
	for _, sample := range samples {
		if sampleMatchesCurrentProbeMode(sample, node) {
			out = append(out, sample)
		}
	}
	return out
}

func sampleMatchesCurrentProbeMode(sample model.NodeSample, node model.ProxyNode) bool {
	expected := currentProbeModeForNode(node)
	if expected == model.ProbeModeEntry {
		return sample.ProbeMode == "" || sample.ProbeMode == model.ProbeModeEntry
	}
	return sample.ProbeMode == expected
}

func currentProbeModeForNode(node model.ProxyNode) string {
	if len(node.Outbound) > 0 {
		if _, ok := node.Outbound["type"].(string); ok {
			return model.ProbeModeProxy204
		}
	}
	return model.ProbeModeEntry
}

func publicNodes(nodes []model.ProxyNode) []model.ProxyNode {
	out := make([]model.ProxyNode, len(nodes))
	for i, node := range nodes {
		out[i] = publicNode(node)
	}
	return out
}

func publicNode(node model.ProxyNode) model.ProxyNode {
	node.Outbound = nil
	return node
}

func publicBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.Split(forwarded, ",")[0]
	}
	host := r.Host
	if forwardedHost := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwardedHost != "" {
		host = strings.Split(forwardedHost, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host)
}

func (s *Server) controllerAccessURLs(r *http.Request) []string {
	requestURL := publicBaseURL(r)
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return []string{requestURL}
	}
	port := parsed.Port()
	if port == "" {
		if cfg := s.configSnapshot(); cfg.WebPort > 0 {
			port = strconv.Itoa(cfg.WebPort)
		}
	}
	return uniqueStrings(append(detectControllerURLs(parsed.Scheme, port), requestURL)...)
}

func detectControllerInterfaceURLs(scheme, port string) []string {
	if scheme == "" {
		scheme = "http"
	}
	var zerotier []string
	var lan []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		name := strings.ToLower(iface.Name)
		isZeroTier := strings.Contains(name, "zerotier") || strings.HasPrefix(name, "zt") || strings.Contains(name, "-zt")
		for _, addr := range addrs {
			ip := interfaceIPv4(addr)
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			value := scheme + "://" + hostPort(ip.String(), port)
			if isZeroTier {
				zerotier = append(zerotier, value)
			} else {
				lan = append(lan, value)
			}
		}
	}
	return uniqueStrings(append(zerotier, lan...)...)
}

func interfaceIPv4(addr net.Addr) net.IP {
	switch typed := addr.(type) {
	case *net.IPNet:
		return typed.IP.To4()
	case *net.IPAddr:
		return typed.IP.To4()
	default:
		return nil
	}
}

func hostPort(host, port string) string {
	if strings.TrimSpace(port) == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func uniqueStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimRight(strings.TrimSpace(value), "/")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func shellValue(value string, dynamic bool) string {
	if dynamic {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return shellQuote(value)
}

func isHostnamePlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	return value == "agent-$(hostname)" || value == "agent-$(hostname -s)" || value == "$(hostname)" || value == "$(hostname -s)"
}

func invalidAgentIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || isHostnamePlaceholder(value) || strings.Contains(value, "$(")
}

func agentInstallScript() string {
	return `#!/usr/bin/env bash
set -euo pipefail

CONTROLLER_URL=""
FALLBACK_CONTROLLER_URL=""
TOKEN=""
AGENT_ID=""
AGENT_NAME=""
PROBE_SOURCE=""
CARRIER="unknown"
INTERVAL="300"
GITHUB_BASE="${GITHUB_BASE:-https://github.com/your-org/node-latency-watch/releases/latest/download}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/node-latency-watch}"
SERVICE_NAME="${SERVICE_NAME:-node-latency-agent}"

usage() {
  cat <<USAGE
Usage: install.sh --controller URL [--fallback-controller URL] --token TOKEN --id ID [--name NAME] [--probe-source SOURCE] [--carrier telecom|unicom|mobile|unknown] [--interval SECONDS]
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --controller) CONTROLLER_URL="${2:-}"; shift 2 ;;
    --fallback-controller) FALLBACK_CONTROLLER_URL="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --id) AGENT_ID="${2:-}"; shift 2 ;;
    --name) AGENT_NAME="${2:-}"; shift 2 ;;
    --probe-source) PROBE_SOURCE="${2:-}"; shift 2 ;;
    --carrier) CARRIER="${2:-unknown}"; shift 2 ;;
    --interval) INTERVAL="${2:-300}"; shift 2 ;;
    --github-base) GITHUB_BASE="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage; exit 1 ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  echo "Please run as root, for example: curl -fsSL ... | sudo bash -s -- ..." >&2
  exit 1
fi
if [ -z "$CONTROLLER_URL" ] || [ -z "$TOKEN" ] || [ -z "$AGENT_ID" ]; then
  if [ -z "$CONTROLLER_URL" ] || [ -z "$TOKEN" ]; then
    usage >&2
    exit 1
  fi
fi

HOST_SHORT="$(hostname -s 2>/dev/null || hostname)"
if [ -z "$AGENT_ID" ] || [ "$AGENT_ID" = 'agent-$(hostname)' ] || [ "$AGENT_ID" = 'agent-$(hostname -s)' ]; then
  AGENT_ID="agent-$HOST_SHORT"
fi
if [ -z "$AGENT_NAME" ] || [ "$AGENT_NAME" = '新 Agent' ] || [ "$AGENT_NAME" = '$(hostname)' ] || [ "$AGENT_NAME" = '$(hostname -s)' ] || [ "$AGENT_NAME" = 'agent-$(hostname)' ] || [ "$AGENT_NAME" = 'agent-$(hostname -s)' ]; then
  AGENT_NAME="$AGENT_ID"
fi

AGENT_NAME="${AGENT_NAME:-$AGENT_ID}"
PROBE_SOURCE="${PROBE_SOURCE:-$AGENT_NAME}"
CONTROLLER_URL="${CONTROLLER_URL%/}"
FALLBACK_CONTROLLER_URL="${FALLBACK_CONTROLLER_URL%/}"
if [ "$FALLBACK_CONTROLLER_URL" = "$CONTROLLER_URL" ]; then
  FALLBACK_CONTROLLER_URL=""
fi

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) TARGET="linux-amd64" ;;
  aarch64|arm64) TARGET="linux-arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

download() {
  url="$1"
  dst="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL --connect-timeout 8 --retry 2 -o "$dst" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -O "$dst" "$url"
  else
    echo "curl or wget is required" >&2
    return 1
  fi
}

BIN_TMP="$TMP/node-latency-agent"
PRIMARY_URL="$CONTROLLER_URL/api/agent/download/$TARGET"
CONTROLLER_FALLBACK_URL=""
if [ -n "$FALLBACK_CONTROLLER_URL" ]; then
  CONTROLLER_FALLBACK_URL="$FALLBACK_CONTROLLER_URL/api/agent/download/$TARGET"
fi
GITHUB_URL="${GITHUB_BASE%/}/node-latency-agent-$TARGET"

echo "[1/4] Downloading agent binary from controller: $PRIMARY_URL"
if ! download "$PRIMARY_URL" "$BIN_TMP"; then
  if [ -n "$CONTROLLER_FALLBACK_URL" ]; then
    echo "[warn] Primary controller download failed, trying fallback controller: $CONTROLLER_FALLBACK_URL"
    download "$CONTROLLER_FALLBACK_URL" "$BIN_TMP" || {
      echo "[warn] Fallback controller download failed, falling back to GitHub: $GITHUB_URL"
      download "$GITHUB_URL" "$BIN_TMP"
    }
  else
    echo "[warn] Controller download failed, falling back to GitHub: $GITHUB_URL"
    download "$GITHUB_URL" "$BIN_TMP"
  fi
fi

install -d "$INSTALL_DIR" "$CONFIG_DIR"
install -m 0755 "$BIN_TMP" "$INSTALL_DIR/node-latency-agent"

cat > "$CONFIG_DIR/agent.yaml" <<YAML
node_role: agent

agent:
  id: "$AGENT_ID"
  name: "$AGENT_NAME"
  controller_url: "$CONTROLLER_URL"
  controller_urls:
    - "$CONTROLLER_URL"
YAML
if [ -n "$FALLBACK_CONTROLLER_URL" ]; then
  cat >> "$CONFIG_DIR/agent.yaml" <<YAML
    - "$FALLBACK_CONTROLLER_URL"
YAML
fi
cat >> "$CONFIG_DIR/agent.yaml" <<YAML
  token: "$TOKEN"
  probe_source: "$PROBE_SOURCE"
  carrier: "$CARRIER"
  report_interval_seconds: $INTERVAL

probe:
  timeout_seconds: 5
  attempts: 3
  max_concurrency: 32
  tls_mode: auto
  test_url: "http://www.gstatic.com/generate_204"
YAML
chmod 0600 "$CONFIG_DIR/agent.yaml"

cat > "/etc/systemd/system/$SERVICE_NAME.service" <<SERVICE
[Unit]
Description=Node Latency Watch Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/node-latency-agent $CONFIG_DIR/agent.yaml
Restart=always
RestartSec=8
User=root

[Install]
WantedBy=multi-user.target
SERVICE

echo "[2/4] Reloading systemd"
systemctl daemon-reload
echo "[3/4] Enabling $SERVICE_NAME"
systemctl enable "$SERVICE_NAME" >/dev/null
echo "[4/4] Starting $SERVICE_NAME"
systemctl restart "$SERVICE_NAME"
systemctl --no-pager --full status "$SERVICE_NAME" || true

echo "Installed. Config: $CONFIG_DIR/agent.yaml"
`
}
