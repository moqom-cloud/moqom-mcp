// Command moqom-mcp is a Model Context Protocol server for operating a MOQOM
// tenant: rooms, join tokens, moderation, billing, API keys, and docs search.
//
// By default it speaks MCP over stdio, which is what desktop clients launch.
// With --http it serves Streamable HTTP instead, behind a bearer token.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moqom-cloud/moqom-mcp/internal/billing"
	"github.com/moqom-cloud/moqom-mcp/internal/config"
	"github.com/moqom-cloud/moqom-mcp/internal/docs"
	"github.com/moqom-cloud/moqom-mcp/internal/mcpserver"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "moqom-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("moqom-mcp", flag.ContinueOnError)
	httpAddr := flags.String("http", "", "serve Streamable HTTP on this address (e.g. :8765) instead of stdio; requires MOQOM_MCP_TOKEN")
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: moqom-mcp [--http ADDR]\n\n"+
			"Environment:\n"+
			"  MOQOM_API_KEY      backend API key, mqk_... (required)\n"+
			"  MOQOM_API_URL      billing REST API (default %s)\n"+
			"  MOQOM_CONTROL_URL  gRPC admin endpoint (default %s)\n"+
			"  MOQOM_TENANT       tenant id (optional; looked up from the key)\n"+
			"  MOQOM_DOCS_URL     documentation site (default %s)\n"+
			"  MOQOM_MCP_TOKEN    bearer token HTTP clients must send (required with --http)\n\nFlags:\n",
			config.DefaultAPIURL, config.DefaultControlURL, config.DefaultDocsURL)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Println("moqom-mcp", version)
		return nil
	}

	// Logs go to stderr: in stdio mode stdout is the protocol.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if *httpAddr != "" && cfg.MCPToken == "" {
		return errors.New("--http requires MOQOM_MCP_TOKEN, the bearer token clients must present")
	}

	userAgent := "moqom-mcp/" + version
	billingClient, err := billing.New(cfg.APIURL, cfg.APIKey, billing.WithUserAgent(userAgent))
	if err != nil {
		return err
	}
	docsClient, err := docs.New(cfg.DocsURL, nil)
	if err != nil {
		return err
	}
	target, insecure, err := config.GRPCTarget(cfg.ControlURL)
	if err != nil {
		return err
	}
	if insecure {
		logger.Warn("MOQOM_CONTROL_URL uses http://; the API key will be sent without TLS", "target", target)
	}
	admin := mcpserver.NewSDKAdmin(target, insecure, cfg.APIKey, cfg.Tenant, func(ctx context.Context) (string, error) {
		summary, err := billingClient.Summary(ctx)
		return summary.Tenant.ID, err
	})
	defer admin.Close()

	keyID, _ := config.KeyID(cfg.APIKey)
	server := mcpserver.New(mcpserver.Deps{
		Admin:        admin,
		Billing:      billingClient,
		Docs:         docsClient,
		CurrentKeyID: keyID,
		Version:      version,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *httpAddr == "" {
		logger.Info("serving MCP over stdio", "version", version, "key_id", keyID)
		return server.Run(ctx, &mcp.StdioTransport{})
	}
	return serveHTTP(ctx, logger, server, *httpAddr, cfg.MCPToken, keyID)
}

func serveHTTP(ctx context.Context, logger *slog.Logger, server *mcp.Server, addr, token, keyID string) error {
	addr, err := bindAddress(addr)
	if err != nil {
		return err
	}
	if host, _, _ := net.SplitHostPort(addr); !isLoopback(host) {
		logger.Warn("listening beyond loopback; anyone who can reach this address and holds MOQOM_MCP_TOKEN can operate the tenant", "addr", addr)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		SessionTimeout: 30 * time.Minute,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(token, handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })

	httpServer := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger.Info("serving MCP over Streamable HTTP", "url", "http://"+listener.Addr().String()+"/mcp", "version", version, "key_id", keyID)

	errs := make(chan error, 1)
	go func() { errs <- httpServer.Serve(listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

// bindAddress defaults an address with no host to loopback, so `--http :8765`
// is not reachable from the network unless a host is given explicitly.
func bindAddress(addr string) (string, error) {
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("--http %q: %w", addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// requireBearer rejects requests without the configured token, compared in
// constant time.
func requireBearer(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="moqom-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
