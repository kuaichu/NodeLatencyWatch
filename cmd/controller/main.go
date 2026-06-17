package main

import (
	"log"
	"os"
	"os/signal"

	"node-latency-watch/internal/config"
	"node-latency-watch/internal/controller"
)

func main() {
	configPath := "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if !cfg.IsControllerMode() {
		log.Fatalf("controller requires node_role: controller")
	}
	srv, err := controller.New(configPath, cfg)
	if err != nil {
		log.Fatalf("start controller: %v", err)
	}
	defer srv.Close()
	if err := srv.RefreshProviders(); err != nil {
		log.Printf("[providers] initial refresh failed: %v", err)
	}
	srv.StartProviderRefreshLoop()
	go srv.Start()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh
	log.Printf("[controller] shutting down")
}
