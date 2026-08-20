package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/remask/remask-core/internal/app"
	"github.com/remask/remask-core/internal/forwardproxy"
	"github.com/remask/remask-core/internal/model"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:17680", "HTTP listen address")
	proxyAddr := flag.String("proxy-addr", "127.0.0.1:17681", "AI proxy listen address")
	forwardProxyAddr := flag.String("forward-proxy-addr", "127.0.0.1:17682", "HTTP/HTTPS and SOCKS5 proxy gateway listen address")
	modelsDir := flag.String("models-dir", "models", "managed model directory")
	builtinModelsDir := flag.String("builtin-models-dir", "", "read-only built-in model directory")
	activeModel := flag.String("active-model", os.Getenv("REMASK_ACTIVE_MODEL"), "model ID to load before serving requests")
	onnxRuntimeLibrary := flag.String("onnxruntime-lib", "", "path to the ONNX Runtime shared library")
	onnxProvider := flag.String("onnx-provider", envOr("REMASK_ONNX_PROVIDER", "auto"), "ONNX execution provider: auto, cpu, coreml, cuda, directml, tensorrt, rocm, or openvino")
	onnxDevice := flag.Int("onnx-device", envInt("REMASK_ONNX_DEVICE", 0), "GPU device index for the ONNX execution provider")
	dataDir := flag.String("data-dir", os.Getenv("REMASK_DATA_DIR"), "directory for settings, upstreams, and masked audit logs")
	selfTest := flag.Bool("self-test", false, "initialize the runtime and active model, then exit")
	flag.Parse()

	logger := log.New(os.Stderr, "remask-core ", log.LstdFlags|log.LUTC)
	if *addr == *proxyAddr || *addr == *forwardProxyAddr || *proxyAddr == *forwardProxyAddr {
		logger.Printf("management, API gateway, and proxy gateway addresses must be different")
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
	runtime, err := model.NewRuntimeWithOptions(*onnxRuntimeLibrary, model.RuntimeOptions{Provider: *onnxProvider, DeviceID: *onnxDevice})
	if err != nil {
		logger.Printf("initialize model runtime: %v", err)
		os.Exit(1)
	}
	application, err := app.NewWithOptions(logger, app.Options{ModelsDir: *modelsDir, BuiltinModelsDir: *builtinModelsDir, Runtime: runtime, ActiveModel: *activeModel, DataDir: *dataDir})
	if err != nil {
		logger.Printf("initialize application: %v", err)
		os.Exit(1)
	}
	if !application.AdvancedLicense() {
		for name, value := range map[string]*string{"proxy": proxyAddr, "forward proxy": forwardProxyAddr} {
			host, port, splitErr := net.SplitHostPort(*value)
			ip := net.ParseIP(host)
			if splitErr == nil && ip != nil && !ip.IsLoopback() {
				logger.Printf("%s LAN binding requires an advanced license; using loopback", name)
				*value = net.JoinHostPort("127.0.0.1", port)
			}
		}
	}
	if *selfTest {
		logger.Printf("self-test passed runtime=%s active_model=%s", runtime.Name(), *activeModel)
		return
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
	forwardProxyServer := &forwardproxy.Server{
		Addr:              *forwardProxyAddr,
		Handler:           application.ForwardProxy(),
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
		if err := forwardProxyServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("shutdown proxy gateway server: %v", err)
		}
	}()

	serverErrors := make(chan error, 3)
	go func() { serverErrors <- server.ListenAndServe() }()
	go func() { serverErrors <- proxyServer.ListenAndServe() }()
	go func() { serverErrors <- forwardProxyServer.ListenAndServe() }()
	logger.Printf("ready address=%s proxy_address=%s forward_proxy_address=%s ca_certificate=%s", *addr, *proxyAddr, *forwardProxyAddr, application.ProxyAuthorityStatus().CertificatePath)
	if err := <-serverErrors; err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
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
	// Packaged desktop builds keep the runtime under resources next to the
	// executable. Resolve that location before falling back to the platform
	// loader's default DLL search path, which may contain an incompatible ORT.
	if executable, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executable)
		candidates = append([]string{
			filepath.Join(executableDir, "resources", "onnxruntime", filename),
		}, candidates...)
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
