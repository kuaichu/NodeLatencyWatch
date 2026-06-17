package main

import (
	"log"
	"os"

	"node-latency-watch/internal/agent"
	"node-latency-watch/internal/config"
)

func main() {
	configPath := "agent.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if !cfg.IsAgentMode() {
		log.Fatalf("agent requires node_role: agent")
	}
	agent.Run(cfg)
}
