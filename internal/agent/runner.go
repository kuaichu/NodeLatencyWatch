package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"node-latency-watch/internal/config"
	"node-latency-watch/internal/model"
	"node-latency-watch/internal/probe"
)

func Run(cfg *config.Config) {
	normalizeAgentIdentity(cfg)
	client := &http.Client{Timeout: 60 * time.Second}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	log.Printf("[agent] starting %s (%s) -> %s", cfg.Agent.ID, model.CarrierLabel(cfg.Agent.Carrier), cfg.Agent.ControllerURL)
	for {
		started := time.Now()
		job, err := fetchJob(client, cfg)
		if err != nil {
			log.Printf("[agent] fetch job failed: %v", err)
			if waitOrStop(sigCh, 30*time.Second) {
				return
			}
			continue
		}
		report := runJob(cfg, job)
		if err := postReport(client, cfg, report); err != nil {
			log.Printf("[agent] post report failed: %v", err)
		} else {
			log.Printf("[agent] report sent nodes=%d", len(report.Results))
		}
		interval := time.Duration(cfg.Agent.ReportIntervalSec) * time.Second
		if interval <= 0 {
			interval = time.Duration(job.Probe.IntervalSeconds) * time.Second
		}
		if interval <= 0 {
			interval = 5 * time.Minute
		}
		if elapsed := time.Since(started); elapsed < interval {
			if waitOrStop(sigCh, interval-elapsed) {
				return
			}
		}
	}
}

func fetchJob(client *http.Client, cfg *config.Config) (*model.JobResponse, error) {
	req, err := http.NewRequest("GET", endpoint(cfg.Agent.ControllerURL, "/api/agent/jobs"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Agent.Token)
	req.Header.Set("X-Agent-ID", cfg.Agent.ID)
	req.Header.Set("X-Agent-Name", agentName(cfg))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controller returned %s", resp.Status)
	}
	var job model.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func runJob(cfg *config.Config, job *model.JobResponse) model.AgentReport {
	started := time.Now()
	agentName := firstNonEmpty(job.AgentName, agentName(cfg))
	carrier := model.NormalizeCarrier(firstNonEmpty(job.AgentCarrier, cfg.Agent.Carrier))
	probeCfg := job.Probe
	if probeCfg.Attempts <= 0 {
		probeCfg.Attempts = cfg.Probe.Attempts
	}
	if probeCfg.TimeoutSeconds <= 0 {
		probeCfg.TimeoutSeconds = cfg.Probe.TimeoutSeconds
	}
	if probeCfg.MaxConcurrency <= 0 {
		probeCfg.MaxConcurrency = cfg.Probe.MaxConcurrency
	}
	if probeCfg.TLSMode == "" {
		probeCfg.TLSMode = cfg.Probe.TLSMode
	}
	log.Printf("[agent] probing %d node(s), attempts=%d concurrency=%d tls=%s", len(job.Nodes), probeCfg.Attempts, probeCfg.MaxConcurrency, probeCfg.TLSMode)
	results := probe.Run(job.Nodes, probeCfg)
	okCount := 0
	for _, result := range results {
		if result.Success {
			okCount++
		}
	}
	log.Printf("[agent] probe finished success=%d/%d", okCount, len(results))
	return model.AgentReport{
		AgentID:      cfg.Agent.ID,
		AgentName:    agentName,
		Carrier:      carrier,
		CarrierLabel: model.CarrierLabel(carrier),
		ProbeSource:  firstNonEmpty(job.AgentProbeSource, cfg.Agent.ProbeSource),
		StartedAt:    started,
		FinishedAt:   time.Now(),
		Results:      results,
	}
}

func postReport(client *http.Client, cfg *config.Config, report model.AgentReport) error {
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", endpoint(cfg.Agent.ControllerURL, "/api/agent/reports"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Agent.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("controller returned %s", resp.Status)
	}
	return nil
}

func waitOrStop(sigCh <-chan os.Signal, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case sig := <-sigCh:
		log.Printf("[agent] received %v, exiting", sig)
		return true
	case <-timer.C:
		return false
	}
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

func agentName(cfg *config.Config) string {
	return firstNonEmpty(cfg.Agent.Name, cfg.Agent.ID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeAgentIdentity(cfg *config.Config) {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	if isHostnamePlaceholder(cfg.Agent.ID) {
		cfg.Agent.ID = "agent-" + host
	}
	if strings.TrimSpace(cfg.Agent.Name) == "" || cfg.Agent.Name == "新 Agent" || isHostnamePlaceholder(cfg.Agent.Name) {
		cfg.Agent.Name = cfg.Agent.ID
	}
}

func isHostnamePlaceholder(value string) bool {
	value = strings.TrimSpace(value)
	return value == "agent-$(hostname)" || value == "agent-$(hostname -s)" || value == "$(hostname)" || value == "$(hostname -s)"
}
