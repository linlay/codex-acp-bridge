package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codex-acp-bridge/internal/acpbridge"
	"codex-acp-bridge/internal/codexacp"
	"codex-acp-bridge/internal/config"
	"codex-acp-bridge/internal/desktopbridge"
	"codex-acp-bridge/internal/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == config.CodexBackendModeArg {
		if err := codexacp.Run(os.Args[2:]); err != nil {
			log.Fatalf("run codex ACP backend: %v", err)
		}
		return
	}

	envPath := flag.String("env", "", "Path to codex-acp-bridge dotenv config")
	flag.Parse()

	cfg, err := config.Load(*envPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	manager := acpbridge.NewManager(cfg)
	defer manager.Close()

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: server.New(cfg, manager), ReadHeaderTimeout: 10 * time.Second}

	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	var bridgeBeforeQuit <-chan struct{}
	if bridgeClient, ok := desktopbridge.NewFromEnv("codex", desktopbridge.BaseURLFromListenAddr(cfg.ListenAddr), 300000, log.Default()); ok {
		bridgeBeforeQuit = bridgeClient.BeforeQuit()
		bridgeClient.SetAuthToken(cfg.AuthToken)
		go bridgeClient.Run(bridgeCtx)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("[codex-acp-bridge] listening on %s", cfg.ListenAddr)
		errCh <- srv.ListenAndServe()
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-stop:
		log.Printf("[codex-acp-bridge] shutdown signal: %s", sig)
	case <-bridgeBeforeQuit:
		log.Printf("[codex-acp-bridge] desktop bridge requested shutdown")
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
		return
	}

	bridgeCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("[codex-acp-bridge] graceful shutdown failed: %v", err)
	}
}
