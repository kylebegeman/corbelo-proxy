package config

import (
	"testing"
	"time"
)

func TestLoadFromEnvDefaultsAndValidation(t *testing.T) {
	t.Setenv("CORBELO_PROXY_ADDR", "")
	t.Setenv("CORBELO_PROXY_UPSTREAM_URL", "")
	t.Setenv("CORBELO_PROXY_ALLOWED_ORIGINS", "")
	t.Setenv("CORBELO_PROXY_MODELS", "gpt-5.4, qwen2.5-coder:14b ")
	t.Setenv("CORBELO_PROXY_COOLDOWN_DEFAULT_SEC", "45")

	cfg := LoadFromEnv()
	if cfg.ListenAddr != "127.0.0.1:8317" {
		t.Fatalf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.UpstreamURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("UpstreamURL = %q", cfg.UpstreamURL)
	}
	if cfg.DefaultCooldown != 45*time.Second {
		t.Fatalf("DefaultCooldown = %v", cfg.DefaultCooldown)
	}
	if len(cfg.StaticModels) != 2 || cfg.StaticModels[0] != "gpt-5.4" || cfg.StaticModels[1] != "qwen2.5-coder:14b" {
		t.Fatalf("StaticModels = %#v", cfg.StaticModels)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate default config: %v", err)
	}

	bad := cfg
	bad.UpstreamURL = "ftp://invalid.example"
	if err := bad.Validate(); err == nil {
		t.Fatal("expected Validate error for non-http upstream URL")
	}
}
