package config

import (
	"fmt"
	"strings"
	"testing"
)

const key = "mqk_0123456789abcdef_c2VjcmV0LXNlY3JldA"

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"MOQOM_API_KEY": key}))
	if err != nil {
		t.Fatal(err)
	}
	if c.APIURL != DefaultAPIURL || c.ControlURL != DefaultControlURL || c.DocsURL != DefaultDocsURL || c.Tenant != "" {
		t.Errorf("defaults = %v", c)
	}
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]map[string]string{
		"missing key":   {},
		"not a mqk key": {"MOQOM_API_KEY": "sk_live_123"},
		"short id":      {"MOQOM_API_KEY": "mqk_abc_secret"},
		"bad api url":   {"MOQOM_API_KEY": key, "MOQOM_API_URL": "ftp://x"},
		"bad control":   {"MOQOM_API_KEY": key, "MOQOM_CONTROL_URL": "grpc://x"},
	}
	for name, values := range cases {
		if _, err := Load(env(values)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "c2VjcmV0") {
			t.Errorf("%s: error leaks the key: %v", name, err)
		}
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	c, err := Load(env(map[string]string{"MOQOM_API_KEY": key, "MOQOM_MCP_TOKEN": "hunter2"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(rendered, "c2VjcmV0") || strings.Contains(rendered, "hunter2") {
			t.Errorf("rendering leaks a secret: %s", rendered)
		}
	}
}

func TestGRPCTarget(t *testing.T) {
	cases := []struct {
		in       string
		target   string
		insecure bool
	}{
		{"https://api.moqom.cloud", "api.moqom.cloud:443", false},
		{"https://api.moqom.cloud:8443/", "api.moqom.cloud:8443", false},
		{"api.moqom.cloud", "api.moqom.cloud:443", false},
		{"http://localhost:9090", "localhost:9090", true},
	}
	for _, c := range cases {
		target, insecure, err := GRPCTarget(c.in)
		if err != nil || target != c.target || insecure != c.insecure {
			t.Errorf("GRPCTarget(%q) = %q, %v, %v", c.in, target, insecure, err)
		}
	}
}

func TestKeyID(t *testing.T) {
	if id, ok := KeyID(key); !ok || id != "0123456789abcdef" {
		t.Errorf("KeyID = %q, %v", id, ok)
	}
}
