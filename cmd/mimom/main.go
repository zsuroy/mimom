package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

var version = "0.1.2"

func main() {
	var (
		configPath = flag.String("config", "config.yaml", "config file path")
		showVer    = flag.Bool("version", false, "show version")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("MiMom %s\n", version)
		os.Exit(0)
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[FATAL] load config: %v", err)
	}

	printBanner(cfg)

	stats := NewStats()
	handler := NewProxyHandler(cfg)
	handler.stats = stats
	responsesHandler := NewResponsesHandler(cfg, handler.reason)
	responsesHandler.stats = stats
	dashHandler := NewDashboardHandler(cfg, stats, handler.reason)

	mux := http.NewServeMux()
	mux.Handle("/v1/responses", responsesHandler) // Responses API → Chat Completions 转换
	mux.Handle("/v1/", handler)                   // OpenAI / Anthropic 原样透传
	mux.Handle("/dashboard", dashHandler)
	mux.Handle("/dashboard/api", dashHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
}

func printBanner(cfg *Config) {
	fmt.Printf("MiMom %s — MiMo API Proxy\n\n", version)
	log.Printf("config: loaded %d backend(s)", len(cfg.Backends))
	for name, b := range cfg.Backends {
		models := make([]string, 0, len(b.Models))
		for clientName := range b.Models {
			models = append(models, clientName)
		}
		log.Printf("  [%s] %s → %v", name, b.BaseURL, models)
	}
	if cfg.Server.APIKey != "" {
		log.Printf("auth: enabled")
	} else {
		log.Printf("auth: disabled (open access)")
	}
}
