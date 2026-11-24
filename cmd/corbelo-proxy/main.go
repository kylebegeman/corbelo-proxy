package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kylebegeman/corbelo-proxy/internal/config"
	"github.com/kylebegeman/corbelo-proxy/internal/proxy"
)

func main() {
	cfg := config.LoadFromEnv()

	addrFlag := flag.String("addr", cfg.ListenAddr, "Listen address (loopback recommended, e.g. 127.0.0.1:8317)")
	upstreamFlag := flag.String("upstream", cfg.UpstreamURL, "Upstream OpenAI/Anthropic-compatible base URL (e.g. http://127.0.0.1:11434/v1)")
	originsFlag := flag.String("allowed-origins", strings.Join(cfg.AllowedOrigins, ","), "Comma-separated browser CORS allowlist")
	modelsFlag := flag.String("models", strings.Join(cfg.StaticModels, ","), "Optional comma-separated extra model IDs")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("corbelo-proxy %s\n", proxy.Version)
		return
	}

	cfg.ListenAddr = strings.TrimSpace(*addrFlag)
	cfg.UpstreamURL = strings.TrimRight(strings.TrimSpace(*upstreamFlag), "/")
	if strings.TrimSpace(*originsFlag) != "" {
		var origins []string
		for _, part := range strings.Split(*originsFlag, ",") {
			if p := strings.TrimSpace(part); p != "" {
				origins = append(origins, p)
			}
		}
		cfg.AllowedOrigins = origins
	}
	if strings.TrimSpace(*modelsFlag) != "" {
		var models []string
		for _, part := range strings.Split(*modelsFlag, ",") {
			if p := strings.TrimSpace(part); p != "" {
				models = append(models, p)
			}
		}
		cfg.StaticModels = models
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler, err := proxy.NewServer(cfg, nil, logger)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("corbelo-proxy listening",
			"addr", cfg.ListenAddr,
			"allowed_origins", strings.Join(cfg.AllowedOrigins, ","),
			"auth_required", cfg.ClientAPIKey != "",
			"version", proxy.Version,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	logger.Info("corbelo-proxy stopped")
}
