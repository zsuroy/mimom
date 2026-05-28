package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
server:
  port: 9090
  api_key: "sk-test"
  timeout: 30
backends:
  main:
    type: openai
    base_url: "https://api.example.com/v1"
    api_key: "bk-key"
    models:
      gpt-4: gpt-4-turbo
`), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 9090 {
		t.Errorf("port: got %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.APIKey != "sk-test" {
		t.Errorf("api_key: got %q", cfg.Server.APIKey)
	}
	if cfg.Server.Timeout != 30 {
		t.Errorf("timeout: got %d", cfg.Server.Timeout)
	}
	if len(cfg.Backends) != 1 {
		t.Fatalf("backends: got %d, want 1", len(cfg.Backends))
	}
	b := cfg.Backends["main"]
	if b.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url: got %q", b.BaseURL)
	}
	if b.Models["gpt-4"] != "gpt-4-turbo" {
		t.Errorf("model mapping: got %q", b.Models["gpt-4"])
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`
backends:
  test:
    base_url: "http://localhost:8080"
    api_key: "k"
`), 0644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("default port: got %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Timeout != 120 {
		t.Errorf("default timeout: got %d, want 120", cfg.Server.Timeout)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(`{{{invalid yaml`), 0644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLookupModel_Found(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"b1": {
				BaseURL: "http://a",
				APIKey:  "k1",
				Models:  map[string]string{"client-m": "backend-m"},
			},
		},
	}
	backend, real, ok := cfg.LookupModel("client-m")
	if !ok {
		t.Fatal("expected found")
	}
	if backend.BaseURL != "http://a" {
		t.Errorf("base_url: got %q", backend.BaseURL)
	}
	if real != "backend-m" {
		t.Errorf("real model: got %q", real)
	}
}

func TestLookupModel_NotFound(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"b1": {Models: map[string]string{"a": "b"}},
		},
	}
	_, _, ok := cfg.LookupModel("unknown")
	if ok {
		t.Error("expected not found")
	}
}

func TestLookupModel_MultipleBackends(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"b1": {BaseURL: "http://a", Models: map[string]string{"m1": "r1"}},
			"b2": {BaseURL: "http://b", Models: map[string]string{"m2": "r2"}},
		},
	}
	b1, r1, ok1 := cfg.LookupModel("m1")
	if !ok1 || b1.BaseURL != "http://a" || r1 != "r1" {
		t.Errorf("m1: got %q %q %v", b1.BaseURL, r1, ok1)
	}
	b2, r2, ok2 := cfg.LookupModel("m2")
	if !ok2 || b2.BaseURL != "http://b" || r2 != "r2" {
		t.Errorf("m2: got %q %q %v", b2.BaseURL, r2, ok2)
	}
}

func TestDefaultBackend_Exists(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"first": {BaseURL: "http://first"},
		},
	}
	b := cfg.DefaultBackend()
	if b == nil {
		t.Fatal("expected non-nil")
	}
}

func TestDefaultBackend_Empty(t *testing.T) {
	cfg := &Config{Backends: map[string]BackendDef{}}
	if cfg.DefaultBackend() != nil {
		t.Error("expected nil for empty backends")
	}
}

func TestIsAnthropic(t *testing.T) {
	b := BackendDef{Type: "anthropic"}
	if !b.IsAnthropic() {
		t.Error("expected true for anthropic")
	}
	b2 := BackendDef{Type: "openai"}
	if b2.IsAnthropic() {
		t.Error("expected false for openai")
	}
	b3 := BackendDef{}
	if b3.IsAnthropic() {
		t.Error("expected false for empty type")
	}
}
