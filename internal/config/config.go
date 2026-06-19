package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"node-latency-watch/internal/model"
)

type AgentConfig struct {
	ID                string   `yaml:"id"`
	Name              string   `yaml:"name"`
	ControllerURL     string   `yaml:"controller_url"`
	ControllerURLs    []string `yaml:"controller_urls,omitempty"`
	Token             string   `yaml:"token"`
	ProbeSource       string   `yaml:"probe_source"`
	Carrier           string   `yaml:"carrier"`
	ReportIntervalSec int      `yaml:"report_interval_seconds"`
	ReportTTLSeconds  int      `yaml:"report_ttl_seconds"`
}

type Config struct {
	NodeRole  string            `yaml:"node_role"`
	WebPort   int               `yaml:"web_port"`
	StateDir  string            `yaml:"state_dir"`
	Agent     AgentConfig       `yaml:"agent"`
	Agents    []model.AgentPeer `yaml:"agents"`
	Providers []model.Provider  `yaml:"providers"`
	Probe     model.ProbeConfig `yaml:"probe"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		NodeRole: "controller",
		WebPort:  19200,
		StateDir: "data",
		Probe: model.ProbeConfig{
			IntervalSeconds: 300,
			TimeoutSeconds:  5,
			Attempts:        3,
			MaxConcurrency:  32,
			TLSMode:         "auto",
			TestURL:         "http://www.gstatic.com/generate_204",
		},
		Agent: AgentConfig{
			ReportIntervalSec: 300,
			ReportTTLSeconds:  900,
		},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.Normalize(path); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Save(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	out := *cfg
	configDir := filepath.Dir(path)
	if out.StateDir != "" && filepath.IsAbs(out.StateDir) {
		if rel, err := filepath.Rel(configDir, out.StateDir); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
			out.StateDir = rel
		}
	}
	out.Agent.Carrier = model.NormalizeCarrier(out.Agent.Carrier)
	for i := range out.Agents {
		out.Agents[i].ID = strings.TrimSpace(out.Agents[i].ID)
		out.Agents[i].Name = strings.TrimSpace(out.Agents[i].Name)
		out.Agents[i].ProbeSource = strings.TrimSpace(out.Agents[i].ProbeSource)
		out.Agents[i].Carrier = model.NormalizeCarrier(out.Agents[i].Carrier)
	}
	for i := range out.Providers {
		out.Providers[i].ID = slug(out.Providers[i].ID)
		out.Providers[i].Name = strings.TrimSpace(out.Providers[i].Name)
		out.Providers[i].Category = strings.TrimSpace(out.Providers[i].Category)
		out.Providers[i].SubscriptionURL = strings.TrimSpace(out.Providers[i].SubscriptionURL)
		if out.Providers[i].SubscriptionFile != "" && filepath.IsAbs(out.Providers[i].SubscriptionFile) {
			if rel, err := filepath.Rel(configDir, out.Providers[i].SubscriptionFile); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
				out.Providers[i].SubscriptionFile = rel
			}
		}
	}
	data, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	_, err = Load(path)
	return err
}

func (c *Config) Normalize(configPath string) error {
	c.NodeRole = normalizeRole(c.NodeRole)
	if c.WebPort <= 0 {
		c.WebPort = 19200
	}
	if strings.TrimSpace(c.StateDir) == "" {
		c.StateDir = "data"
	}
	if !filepath.IsAbs(c.StateDir) {
		c.StateDir = filepath.Join(filepath.Dir(configPath), c.StateDir)
	}
	if c.Probe.IntervalSeconds <= 0 {
		c.Probe.IntervalSeconds = 300
	}
	if c.Probe.TimeoutSeconds <= 0 {
		c.Probe.TimeoutSeconds = 5
	}
	if c.Probe.Attempts <= 0 {
		c.Probe.Attempts = 1
	}
	if c.Probe.MaxConcurrency <= 0 {
		c.Probe.MaxConcurrency = 16
	}
	c.Probe.TLSMode = strings.ToLower(strings.TrimSpace(c.Probe.TLSMode))
	if c.Probe.TLSMode == "" {
		c.Probe.TLSMode = "auto"
	}
	if c.Probe.TLSMode != "auto" && c.Probe.TLSMode != "always" && c.Probe.TLSMode != "never" {
		return fmt.Errorf("probe.tls_mode must be auto, always, or never")
	}
	c.Probe.TestURL = strings.TrimSpace(c.Probe.TestURL)
	if c.Probe.TestURL == "" {
		c.Probe.TestURL = "http://www.gstatic.com/generate_204"
	}
	testURL, err := url.ParseRequestURI(c.Probe.TestURL)
	if err != nil {
		return fmt.Errorf("probe.test_url is invalid: %w", err)
	}
	if testURL.Scheme != "http" && testURL.Scheme != "https" {
		return fmt.Errorf("probe.test_url must use http or https")
	}
	if strings.TrimSpace(testURL.Hostname()) == "" {
		return fmt.Errorf("probe.test_url host is required")
	}
	c.Agent.Carrier = model.NormalizeCarrier(c.Agent.Carrier)
	controllerURLs, err := normalizeControllerURLs(c.Agent.ControllerURL, c.Agent.ControllerURLs)
	if err != nil {
		return err
	}
	c.Agent.ControllerURLs = controllerURLs
	if len(controllerURLs) > 0 {
		c.Agent.ControllerURL = controllerURLs[0]
	}
	if c.IsAgentMode() {
		if strings.TrimSpace(c.Agent.ID) == "" {
			return fmt.Errorf("agent.id is required")
		}
		if len(c.Agent.ControllerURLs) == 0 {
			return fmt.Errorf("agent.controller_url is required")
		}
		if strings.TrimSpace(c.Agent.Token) == "" {
			return fmt.Errorf("agent.token is required")
		}
	}
	if c.IsControllerMode() && strings.TrimSpace(c.Agent.Token) == "" {
		return fmt.Errorf("agent.token is required")
	}
	for i := range c.Agents {
		c.Agents[i].ID = strings.TrimSpace(c.Agents[i].ID)
		if c.Agents[i].ID == "" {
			return fmt.Errorf("agents[%d].id is required", i)
		}
		if c.Agents[i].Name == "" {
			c.Agents[i].Name = c.Agents[i].ID
		}
		c.Agents[i].Carrier = model.NormalizeCarrier(c.Agents[i].Carrier)
	}
	for i := range c.Providers {
		c.Providers[i].ID = slug(c.Providers[i].ID)
		if c.Providers[i].ID == "" {
			return fmt.Errorf("providers[%d].id is required", i)
		}
		if c.Providers[i].Name == "" {
			c.Providers[i].Name = c.Providers[i].ID
		}
		c.Providers[i].Category = strings.TrimSpace(c.Providers[i].Category)
		if c.Providers[i].Category == "" {
			c.Providers[i].Category = "默认"
		}
		if c.Providers[i].SubscriptionURL == "" && c.Providers[i].SubscriptionFile == "" {
			return fmt.Errorf("providers[%s] requires subscription_url or subscription_file", c.Providers[i].ID)
		}
		if c.Providers[i].SubscriptionFile != "" && !filepath.IsAbs(c.Providers[i].SubscriptionFile) {
			c.Providers[i].SubscriptionFile = filepath.Join(filepath.Dir(configPath), c.Providers[i].SubscriptionFile)
		}
	}
	return nil
}

func normalizeControllerURLs(primary string, values []string) ([]string, error) {
	raw := make([]string, 0, len(values)+1)
	if strings.TrimSpace(primary) != "" {
		raw = append(raw, primary)
	}
	raw = append(raw, values...)
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(value)
		if err != nil {
			return nil, fmt.Errorf("agent.controller_url is invalid: %w", err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("agent.controller_url must use http or https")
		}
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return nil, fmt.Errorf("agent.controller_url host is required")
		}
		key := strings.TrimRight(value, "/")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out, nil
}

func (c *Config) IsAgentMode() bool {
	return c.NodeRole == "agent"
}

func (c *Config) IsControllerMode() bool {
	return c.NodeRole == "controller"
}

func normalizeRole(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "agent", "worker", "子机":
		return "agent"
	default:
		return "controller"
	}
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	dash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if (r == '-' || r == '_' || r == ' ') && !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
