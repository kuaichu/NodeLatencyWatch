package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"node-latency-watch/internal/config"
	"node-latency-watch/internal/model"
)

func TestFetchJobFallsBackToSecondControllerURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/jobs" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(model.JobResponse{ServerTime: time.Now()})
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{
		Agent: config.AgentConfig{
			ID:             "agent-test",
			Name:           "agent-test",
			Token:          "token",
			ControllerURL:  "http://127.0.0.1:1",
			ControllerURLs: []string{"http://127.0.0.1:1", server.URL},
		},
	}
	client := &http.Client{Timeout: time.Second}

	_, controllerURL, err := fetchJob(client, cfg)
	if err != nil {
		t.Fatalf("fetchJob() error = %v", err)
	}
	if controllerURL != server.URL {
		t.Fatalf("fetchJob() controllerURL = %q, want %q", controllerURL, server.URL)
	}
}

func TestControllerURLsDeduplicateAndTrim(t *testing.T) {
	cfg := &config.Config{
		Agent: config.AgentConfig{
			ControllerURL:  "http://10.0.0.234:19200/",
			ControllerURLs: []string{"http://172.23.93.195:19200", "http://10.0.0.234:19200"},
		},
	}

	got := controllerURLs(cfg)
	want := []string{"http://172.23.93.195:19200", "http://10.0.0.234:19200"}
	if len(got) != len(want) {
		t.Fatalf("controllerURLs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("controllerURLs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
