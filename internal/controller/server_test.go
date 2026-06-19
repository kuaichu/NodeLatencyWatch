package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"node-latency-watch/internal/config"
	"node-latency-watch/internal/model"
)

func TestSampleMatchesCurrentProbeMode(t *testing.T) {
	proxyNode := model.ProxyNode{
		ID:       "proxy",
		Outbound: map[string]any{"type": "vmess"},
	}
	entryNode := model.ProxyNode{ID: "entry"}

	cases := []struct {
		name   string
		node   model.ProxyNode
		mode   string
		expect bool
	}{
		{name: "proxy accepts current mode", node: proxyNode, mode: model.ProbeModeProxy204, expect: true},
		{name: "proxy rejects legacy http-204 mode", node: proxyNode, mode: "http-204", expect: false},
		{name: "proxy rejects entry mode", node: proxyNode, mode: model.ProbeModeEntry, expect: false},
		{name: "entry accepts current mode", node: entryNode, mode: model.ProbeModeEntry, expect: true},
		{name: "entry accepts legacy empty mode", node: entryNode, mode: "", expect: true},
		{name: "entry rejects proxy mode", node: entryNode, mode: model.ProbeModeProxy204, expect: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := model.NodeSample{NodeID: tc.node.ID, ProbeMode: tc.mode}
			if got := sampleMatchesCurrentProbeMode(sample, tc.node); got != tc.expect {
				t.Fatalf("sampleMatchesCurrentProbeMode() = %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestInstallCommandDefaultsToDynamicAgentID(t *testing.T) {
	oldDetect := detectControllerURLs
	detectControllerURLs = func(scheme, port string) []string {
		return []string{"http://172.23.93.195:19200", "http://10.0.0.234:19200"}
	}
	t.Cleanup(func() { detectControllerURLs = oldDetect })

	s := &Server{cfg: &config.Config{
		WebPort: 19200,
		Agent:   config.AgentConfig{Token: "test-token"},
		Agents: []model.AgentPeer{
			{ID: "agent-Yeque", Name: "Yeque_FnOS", ProbeSource: "宁波联通", Carrier: "unicom"},
		},
	}}
	req := httptest.NewRequest(http.MethodGet, "http://controller.local/api/admin/install-command", nil)
	rec := httptest.NewRecorder()

	s.handleInstallCommand(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp installCommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.Contains(resp.Command, "agent-Yeque") {
		t.Fatalf("default install command reused existing agent: %s", resp.Command)
	}
	if !strings.Contains(resp.Command, "http://172.23.93.195:19200/install.sh") {
		t.Fatalf("install command should prefer ZeroTier URL: %s", resp.Command)
	}
	if !strings.Contains(resp.Command, "--fallback-controller 'http://10.0.0.234:19200'") {
		t.Fatalf("install command should include LAN fallback URL: %s", resp.Command)
	}
	if !strings.Contains(resp.Command, `--id "agent-$(hostname -s)"`) {
		t.Fatalf("default install command should use dynamic hostname id: %s", resp.Command)
	}
}
