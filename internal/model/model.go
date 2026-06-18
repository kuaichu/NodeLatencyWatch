package model

import "time"

const (
	ProbeModeEntry    = "entry"
	ProbeModeProxy204 = "proxy-204"
)

type Provider struct {
	ID               string `json:"id" yaml:"id"`
	Name             string `json:"name" yaml:"name"`
	Category         string `json:"category" yaml:"category"`
	SubscriptionURL  string `json:"subscriptionUrl" yaml:"subscription_url"`
	SubscriptionFile string `json:"subscriptionFile" yaml:"subscription_file"`
}

type ProxyNode struct {
	ID         string            `json:"id"`
	ProviderID string            `json:"providerId"`
	Provider   string            `json:"provider"`
	Category   string            `json:"category,omitempty"`
	Name       string            `json:"name"`
	Protocol   string            `json:"protocol"`
	Server     string            `json:"server"`
	Port       int               `json:"port"`
	SNI        string            `json:"sni,omitempty"`
	Host       string            `json:"host,omitempty"`
	Path       string            `json:"path,omitempty"`
	Raw        string            `json:"-"`
	Meta       map[string]string `json:"meta,omitempty"`
	Outbound   map[string]any    `json:"outbound,omitempty"`
}

type AgentPeer struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	ProbeSource string `json:"probeSource" yaml:"probe_source"`
	Carrier     string `json:"carrier" yaml:"carrier"`
}

type ProbeConfig struct {
	IntervalSeconds int    `json:"intervalSeconds" yaml:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeoutSeconds" yaml:"timeout_seconds"`
	Attempts        int    `json:"attempts" yaml:"attempts"`
	MaxConcurrency  int    `json:"maxConcurrency" yaml:"max_concurrency"`
	TLSMode         string `json:"tlsMode" yaml:"tls_mode"`
	TestURL         string `json:"testUrl" yaml:"test_url"`
}

type JobResponse struct {
	ServerTime          time.Time   `json:"serverTime"`
	AgentName           string      `json:"agentName,omitempty"`
	AgentProbeSource    string      `json:"agentProbeSource,omitempty"`
	AgentCarrier        string      `json:"agentCarrier,omitempty"`
	AgentCarrierLabel   string      `json:"agentCarrierLabel,omitempty"`
	Probe               ProbeConfig `json:"probe"`
	Nodes               []ProxyNode `json:"nodes"`
	SubscriptionVersion string      `json:"subscriptionVersion,omitempty"`
}

type NodeResult struct {
	NodeID      string  `json:"nodeId"`
	DNSMs       float64 `json:"dnsMs"`
	TCPMs       float64 `json:"tcpMs"`
	TLSMs       float64 `json:"tlsMs,omitempty"`
	MaxRTTMs    float64 `json:"maxRttMs,omitempty"`
	RTTStdDevMs float64 `json:"rttStdDevMs,omitempty"`
	HTTPMs      float64 `json:"httpMs,omitempty"`
	Attempts    int     `json:"attempts"`
	Successes   int     `json:"successes"`
	LossRate    float64 `json:"lossRate"`
	Success     bool    `json:"success"`
	Error       string  `json:"error,omitempty"`
	ResolvedIP  string  `json:"resolvedIp,omitempty"`
	ProbeMode   string  `json:"probeMode,omitempty"`
}

type AgentReport struct {
	AgentID      string       `json:"agentId"`
	AgentName    string       `json:"agentName"`
	Carrier      string       `json:"carrier"`
	CarrierLabel string       `json:"carrierLabel"`
	ProbeSource  string       `json:"probeSource"`
	StartedAt    time.Time    `json:"startedAt"`
	FinishedAt   time.Time    `json:"finishedAt"`
	Results      []NodeResult `json:"results"`
}

type NodeSample struct {
	Time         time.Time `json:"time"`
	AgentID      string    `json:"agentId"`
	AgentName    string    `json:"agentName"`
	Carrier      string    `json:"carrier"`
	CarrierLabel string    `json:"carrierLabel"`
	ProbeSource  string    `json:"probeSource"`
	ProviderID   string    `json:"providerId"`
	Provider     string    `json:"provider"`
	Category     string    `json:"category,omitempty"`
	NodeID       string    `json:"nodeId"`
	NodeName     string    `json:"nodeName"`
	Protocol     string    `json:"protocol"`
	Server       string    `json:"server"`
	Port         int       `json:"port"`
	DNSMs        float64   `json:"dnsMs"`
	TCPMs        float64   `json:"tcpMs"`
	TLSMs        float64   `json:"tlsMs"`
	MaxRTTMs     float64   `json:"maxRttMs"`
	RTTStdDevMs  float64   `json:"rttStdDevMs"`
	HTTPMs       float64   `json:"httpMs"`
	Attempts     int       `json:"attempts"`
	Successes    int       `json:"successes"`
	LossRate     float64   `json:"lossRate"`
	Success      bool      `json:"success"`
	Error        string    `json:"error,omitempty"`
	ResolvedIP   string    `json:"resolvedIp,omitempty"`
	ProbeMode    string    `json:"probeMode,omitempty"`
}

type NodeOverview struct {
	Node        ProxyNode    `json:"node"`
	Latest      []NodeSample `json:"latest"`
	BestTCPMs   float64      `json:"bestTcpMs"`
	WorstTCPMs  float64      `json:"worstTcpMs"`
	SuccessRate float64      `json:"successRate"`
	LastSeen    time.Time    `json:"lastSeen"`
}

func NormalizeCarrier(value string) string {
	switch value {
	case "telecom", "ct", "电信", "中国电信":
		return "telecom"
	case "unicom", "cu", "联通", "中国联通":
		return "unicom"
	case "mobile", "cm", "移动", "中国移动":
		return "mobile"
	default:
		return "unknown"
	}
}

func CarrierLabel(carrier string) string {
	switch NormalizeCarrier(carrier) {
	case "telecom":
		return "中国电信"
	case "unicom":
		return "中国联通"
	case "mobile":
		return "中国移动"
	default:
		return "未知线路"
	}
}
