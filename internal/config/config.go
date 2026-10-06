// Package config reads moqom-mcp's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Defaults for the endpoints a customer talks to.
const (
	DefaultAPIURL     = "https://api.moqom.cloud"
	DefaultControlURL = "https://api.moqom.cloud"
	DefaultDocsURL    = "https://moqom.dev"
)

// Config is everything the server needs to reach MOQOM.
type Config struct {
	// APIKey is the tenant's backend key, mqk_<id>_<secret>. Never logged.
	APIKey string

	// APIURL is the billing REST API.
	APIURL string

	// ControlURL is the gRPC admin endpoint used by moqom-go.
	ControlURL string

	// Tenant is optional: the key implies it, and it is looked up from the
	// billing API when empty.
	Tenant string

	// DocsURL is the documentation origin for search_docs and get_doc_page.
	DocsURL string

	// MCPToken is the bearer token HTTP clients must present. Only required in
	// HTTP mode.
	MCPToken string
}

// FromEnv reads the configuration from environment variables.
func FromEnv() (Config, error) {
	return Load(os.Getenv)
}

// Load reads the configuration through getenv, so tests need not touch the
// process environment.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		APIKey:     strings.TrimSpace(getenv("MOQOM_API_KEY")),
		APIURL:     orDefault(getenv("MOQOM_API_URL"), DefaultAPIURL),
		ControlURL: orDefault(getenv("MOQOM_CONTROL_URL"), DefaultControlURL),
		Tenant:     strings.TrimSpace(getenv("MOQOM_TENANT")),
		DocsURL:    orDefault(getenv("MOQOM_DOCS_URL"), DefaultDocsURL),
		MCPToken:   strings.TrimSpace(getenv("MOQOM_MCP_TOKEN")),
	}
	if c.APIKey == "" {
		return Config{}, errors.New("MOQOM_API_KEY is required (a backend key, mqk_...)")
	}
	if _, ok := KeyID(c.APIKey); !ok {
		return Config{}, errors.New("MOQOM_API_KEY is not a MOQOM API key: expected mqk_<id>_<secret>")
	}
	for name, value := range map[string]string{"MOQOM_API_URL": c.APIURL, "MOQOM_DOCS_URL": c.DocsURL} {
		if err := checkHTTPURL(value); err != nil {
			return Config{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if _, _, err := GRPCTarget(c.ControlURL); err != nil {
		return Config{}, fmt.Errorf("MOQOM_CONTROL_URL: %w", err)
	}
	return c, nil
}

// String renders the configuration with secrets redacted, so a stray %v in a
// log line cannot leak the key.
func (c Config) String() string {
	return fmt.Sprintf("Config{APIKey:%s APIURL:%s ControlURL:%s Tenant:%q DocsURL:%s MCPToken:%s}",
		redact(c.APIKey), c.APIURL, c.ControlURL, c.Tenant, c.DocsURL, redact(c.MCPToken))
}

// GoString keeps %#v as safe as %v.
func (c Config) GoString() string { return c.String() }

func redact(secret string) string {
	if secret == "" {
		return `""`
	}
	return "[redacted]"
}

// KeyID returns the public ID half of an mqk_ key.
func KeyID(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, "mqk_")
	if !ok {
		return "", false
	}
	id, secret, ok := strings.Cut(rest, "_")
	if !ok || len(id) != 16 || secret == "" {
		return "", false
	}
	return id, true
}

// GRPCTarget turns MOQOM_CONTROL_URL into a gRPC dial target.
//
// https:// and bare host[:port] use TLS on 443 by default; http:// disables
// transport security, which is only sensible for a local control plane.
func GRPCTarget(raw string) (target string, insecure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, errors.New("empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	if u.Host == "" {
		return "", false, fmt.Errorf("%q has no host", raw)
	}
	port := u.Port()
	switch u.Scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		insecure = true
		if port == "" {
			port = "80"
		}
	default:
		return "", false, fmt.Errorf("unsupported scheme %q (use https)", u.Scheme)
	}
	return net.JoinHostPort(u.Hostname(), port), insecure, nil
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%q is not an http(s) URL", raw)
	}
	return nil
}

func orDefault(value, fallback string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return fallback
	}
	return value
}
