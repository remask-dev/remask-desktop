package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/remask/remask-core/internal/app"
	"github.com/remask/remask-core/internal/machinekey"
	"github.com/remask/remask-core/internal/model"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:17680", "HTTP listen address")
	proxyAddr := flag.String("proxy-addr", "127.0.0.1:17681", "AI proxy listen address")
	modelsDir := flag.String("models-dir", "models", "managed model directory")
	activeModel := flag.String("active-model", os.Getenv("REMASK_ACTIVE_MODEL"), "model ID to load before serving requests")
	onnxRuntimeLibrary := flag.String("onnxruntime-lib", "", "path to the ONNX Runtime shared library")
	dataDir := flag.String("data-dir", os.Getenv("REMASK_DATA_DIR"), "directory for settings, upstreams, and masked audit logs")
	flag.Parse()

	logger := log.New(os.Stderr, "remask-core ", log.LstdFlags|log.LUTC)
	if *addr == *proxyAddr {
		logger.Printf("management and proxy addresses must be different")
		os.Exit(1)
	}
	if *dataDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			logger.Printf("resolve home directory: %v", err)
			os.Exit(1)
		}
		remaskDir := filepath.Join(homeDir, ".remask")
		*dataDir = remaskDir
	}
	if *onnxRuntimeLibrary == "" {
		*onnxRuntimeLibrary = discoverRuntimeLibrary()
	}
	if *activeModel == "" {
		const defaultModel = "openai-privacy-filter-q4"
		if _, err := os.Stat(filepath.Join(*modelsDir, defaultModel, "manifest.json")); err == nil {
			*activeModel = defaultModel
		}
	}
	key, err := machinekey.Derive()
	if err != nil {
		logger.Printf("derive machine key: %v", err)
		os.Exit(1)
	}
	runtime, err := model.NewRuntime(*onnxRuntimeLibrary)
	if err != nil {
		logger.Printf("initialize model runtime: %v", err)
		os.Exit(1)
	}
	application, err := app.NewWithOptions(logger, app.Options{ModelsDir: *modelsDir, Runtime: runtime, ActiveModel: *activeModel, DeviceKey: key, DataDir: *dataDir})
	if err != nil {
		logger.Printf("initialize application: %v", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	proxyServer := &http.Server{
		Addr:              *proxyAddr,
		Handler:           application.ProxyHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Printf("shutdown server: %v", err)
		}
		if err := proxyServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("shutdown proxy server: %v", err)
		}
	}()

	serverErrors := make(chan error, 2)
	go func() { serverErrors <- server.ListenAndServe() }()
	go func() { serverErrors <- proxyServer.ListenAndServe() }()
	logger.Printf("ready address=%s proxy_address=%s", *addr, *proxyAddr)
	if err := <-serverErrors; err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func discoverRuntimeLibrary() string {
	if configured := os.Getenv("REMASK_ONNXRUNTIME_LIBRARY"); configured != "" {
		return configured
	}
	filename := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		filename = "libonnxruntime.dylib"
	} else if runtime.GOOS == "windows" {
		filename = "onnxruntime.dll"
	}
	candidates := []string{
		filepath.Join("..", "remask-desktop", "src-tauri", "resources", "onnxruntime", filename),
		filepath.Join("src-tauri", "resources", "onnxruntime", filename),
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err == nil {
			if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
				return absolute
			}
		}
	}
	return ""
}
