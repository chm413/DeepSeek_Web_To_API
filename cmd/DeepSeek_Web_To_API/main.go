package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"DeepSeek_Web_To_API/internal/auth"
	"DeepSeek_Web_To_API/internal/config"
	"DeepSeek_Web_To_API/internal/server"
	"DeepSeek_Web_To_API/internal/webui"
	"DeepSeek_Web_To_API/internal/xrayproxy"
)

func main() {
	os.Exit(run())
}

func run() int {
	config.ApplyLegacyEnvAliases()
	if err := config.LoadDotEnv(); err != nil {
		config.Logger.Warn("[dotenv] load failed", "error", err)
	}
	config.ApplyLegacyEnvAliases()
	config.RefreshLogger()
	app, err := server.NewApp()
	if err != nil {
		config.Logger.Error("server initialization failed", "error", err)
		return 1
	}
	defer app.Close()
	defer xrayproxy.Default().StopAll()
	defer func() {
		if err := app.Store.Close(); err != nil {
			config.Logger.Warn("config store close failed", "error", err)
		}
	}()
	config.RefreshLoggerWithLevel(app.Store.ServerLogLevel())
	webui.EnsureBuiltOnStartup(app.Store)
	if err := auth.ValidateAdminRuntimeSecurity(app.Store); err != nil {
		config.Logger.Error("admin runtime security config invalid", "error", err)
		return 1
	}
	port := app.Store.ServerPort()
	bindAddr := app.Store.ServerBindAddr()
	// Honour the user-configured DEEPSEEK_WEB_TO_API_HTTP_TOTAL_TIMEOUT_SECONDS
	// (default 7200s) for both ReadTimeout and WriteTimeout. Streaming
	// completions for the Pro models can spend several minutes between
	// upstream PoW + reasoning + auto-continue and a hard-coded 120s ceiling
	// would chop the SSE response mid-flight. Setting WriteTimeout=0 disables
	// the standard library write deadline entirely; doing so here would
	// expose us to slowloris-style hangs, so we keep a generous bound that
	// the operator can still tune via env/config.
	httpTotalTimeout := app.Store.HTTPTotalTimeout()
	if httpTotalTimeout <= 0 {
		httpTotalTimeout = 2 * time.Hour
	}

	srv := &http.Server{
		Addr:              bindAddr + ":" + port,
		Handler:           app.Router,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       httpTotalTimeout,
		WriteTimeout:      httpTotalTimeout,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	localURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	lanIP := detectLANIPv4()
	lanURL := ""
	if lanIP != "" {
		lanURL = fmt.Sprintf("http://%s:%s", lanIP, port)
	}

	// Bind before starting the serving goroutine. A pending self-update is only
	// promoted after this succeeds, which gives the Docker entrypoint a simple
	// rollback boundary for a broken candidate binary.
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		config.Logger.Error("server listener initialization failed", "error", err)
		return 1
	}
	serverErr := make(chan error, 1)
	go func() {
		if lanURL != "" {
			config.Logger.Info("starting deepseek-web-to-api", "bind", srv.Addr, "port", port, "local_url", localURL, "lan_url", lanURL, "lan_ip", lanIP)
		} else {
			config.Logger.Info("starting deepseek-web-to-api", "bind", srv.Addr, "port", port, "local_url", localURL)
			config.Logger.Warn("lan ip not detected; check active network interfaces")
		}
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()
	if err := app.ConfirmSelfUpdateStartup(); err != nil {
		config.Logger.Error("self-update candidate readiness confirmation failed", "error", err)
		_ = srv.Close()
		return 1
	}
	updateCtx, stopUpdateChecks := context.WithCancel(context.Background())
	defer stopUpdateChecks()
	app.StartSelfUpdate(updateCtx)

	// Wait for an interrupt signal (Ctrl+C / SIGTERM), a verified self-update
	// restart request, or a listener failure.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)
	exitCode := 0
	select {
	case sig := <-quit:
		config.Logger.Info("shutdown signal received", "signal", sig.String())
	case <-app.SelfUpdateRestartRequests():
		exitCode = app.SelfUpdateRestartExitCode()
		config.Logger.Info("self-update restart requested", "exit_code", exitCode)
	case err := <-serverErr:
		config.Logger.Error("server stopped unexpectedly", "error", err)
		return 1
	}

	// Graceful shutdown: allow up to 10 seconds for in-flight requests to complete.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		config.Logger.Error("graceful shutdown failed, forcing exit", "error", err)
		return 1
	}
	config.Logger.Info("server gracefully stopped")
	return exitCode
}

func detectLANIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			default:
				continue
			}
			ip = ip.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			return ip.String()
		}
	}
	return ""
}
