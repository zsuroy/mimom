package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig          `yaml:"server"`
	Backends map[string]BackendDef `yaml:"backends"`
}

type ServerConfig struct {
	Port    int    `yaml:"port"`
	APIKey  string `yaml:"api_key"`
	Timeout int    `yaml:"timeout"`
}

type BackendDef struct {
	Type    string            `yaml:"type"`     // "openai" (默认) 或 "anthropic"
	BaseURL string            `yaml:"base_url"` // 后端 /v1 地址
	APIKey  string            `yaml:"api_key"`
	Models  map[string]string `yaml:"models"` // client_name → backend_name
}

// IsAnthropic 返回是否为 Anthropic 类型后端。
func (b *BackendDef) IsAnthropic() bool {
	return b.Type == "anthropic"
}

// LookupModel 根据客户端模型名查找后端配置。
func (c *Config) LookupModel(clientModel string) (*BackendDef, string, bool) {
	for _, b := range c.Backends {
		if realName, ok := b.Models[clientModel]; ok {
			return &b, realName, true
		}
	}
	return nil, "", false
}

// LookupModelByType 在指定类型的后端中查找模型。
func (c *Config) LookupModelByType(clientModel, backendType string) (*BackendDef, string, bool) {
	for _, b := range c.Backends {
		if b.Type != backendType {
			continue
		}
		if realName, ok := b.Models[clientModel]; ok {
			return &b, realName, true
		}
	}
	return nil, "", false
}

// DefaultBackend 返回第一个后端。
func (c *Config) DefaultBackend() *BackendDef {
	for _, b := range c.Backends {
		return &b
	}
	return nil
}

// FindAnthropicBackend 返回第一个 Anthropic 类型后端。
func (c *Config) FindAnthropicBackend() *BackendDef {
	for _, b := range c.Backends {
		if b.IsAnthropic() {
			return &b
		}
	}
	return nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Timeout == 0 {
		cfg.Server.Timeout = 120
	}
	return &cfg, nil
}
