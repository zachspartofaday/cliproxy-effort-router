package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	backend := flag.String("backend", "jev", "primary classifier: jev or ocg")
	compare := flag.Bool("compare-ocg", false, "compare OCG alongside Jev; requires explicit router shadow")
	jevURL := flag.String("jev-url", zenEndpoint, "Zen Jev endpoint or local mock /v1/systemone")
	jevModel := flag.String("jev-model", "jev-1.13-free", "upstream Jev model")
	zenCredential := flag.String("zen-credential-file", "", "protected Zen credential path")
	listen := flag.String("listen", "127.0.0.1:8765", "literal loopback listen address")
	cpa := flag.String("cpa-url", "http://127.0.0.1:9444", "CPA loopback origin")
	model := flag.String("model", "ocg/deepseek-v4.1-flash", "OCG alias")
	credential := flag.String("credential-file", "", "systemd credential path (never a key argument)")
	timeout := flag.Duration("timeout", 1200*time.Millisecond, "classifier deadline, shorter than plugin timeout")
	flag.Parse()
	credentialPath := func(explicit, name string) string {
		if explicit != "" {
			return explicit
		}
		if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
			return filepath.Join(dir, name)
		}
		return ""
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "listen must use a literal loopback IP")
		os.Exit(1)
	}
	// Read only service credentials required by the selected backend. In Jev-only
	// mode no CPA client key is loaded or added to the CPA config.
	logger := log.New(os.Stdout, "", 0)
	var handler *server
	switch *backend {
	case "jev":
		var key string
		key, err = readCredential(credentialPath(*zenCredential, "zen-key"))
		if err == nil {
			handler, err = newJevServer(*jevURL, *jevModel, key, *timeout, logger)
		}
	case "ocg":
		if *compare {
			err = fmt.Errorf("comparison requires Jev primary")
			break
		}
		var key string
		key, err = readCredential(credentialPath(*credential, "cpa-key"))
		if err == nil {
			handler, err = newServer(*cpa, *model, key, *timeout, logger)
		}
	default:
		err = fmt.Errorf("invalid backend")
	}
	if err == nil && *compare {
		var key string
		key, err = readCredential(credentialPath(*credential, "cpa-key"))
		if err == nil {
			handler.comparison, err = newServer(*cpa, *model, key, *timeout, nil)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration or credential unavailable")
		os.Exit(1)
	}
	srv := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: *timeout + time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "adapter listener failed")
		os.Exit(1)
	}
}

func readCredential(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("credential unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("unsafe credential")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "", fmt.Errorf("invalid credential")
	}
	key := strings.TrimSpace(string(b))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("invalid credential")
	}
	return key, nil
}
