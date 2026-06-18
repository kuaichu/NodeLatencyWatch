package controller

import (
	"testing"

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
