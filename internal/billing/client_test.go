package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testKey = "mqk_0123456789abcdef_secret"

type recorded struct {
	method, path, auth, contentType string
	body                            map[string]any
}

func fake(t *testing.T, status int, response string) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path = r.Method, r.URL.Path
		rec.auth = r.Header.Get("Authorization")
		rec.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/", testKey, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return client, rec
}

func TestSummary(t *testing.T) {
	client, rec := fake(t, 200, `{
		"tenant": {"id":"acme","name":"Acme","email":"ops@acme.test","status":"active","has_card":true,
			"auto_top_up":{"enabled":true,"below":{"micros":5000000,"display":"$5.00"},"amount":{"micros":20000000,"display":"$20.00"}},
			"created_at":"2026-01-02T03:04:05Z"},
		"balance": {"micros": 12345678, "display": "$12.35"},
		"admitted": true,
		"recent": [{"kind":"top_up","amount":{"micros":20000000,"display":"$20.00"},"reference":"cs_1","at":"2026-10-01T00:00:00Z"}]
	}`)
	summary, err := client.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodGet || rec.path != "/v1/billing" {
		t.Errorf("request = %s %s", rec.method, rec.path)
	}
	if rec.auth != "Bearer "+testKey {
		t.Errorf("Authorization = %q", rec.auth)
	}
	if summary.Tenant.ID != "acme" || !summary.Admitted || summary.Balance.Micros != 12345678 {
		t.Errorf("summary = %+v", summary)
	}
	if !summary.Tenant.AutoTopUp.Enabled || summary.Tenant.AutoTopUp.Below.Display != "$5.00" {
		t.Errorf("auto top-up = %+v", summary.Tenant.AutoTopUp)
	}
	if len(summary.Recent) != 1 || summary.Recent[0].Kind != "top_up" {
		t.Errorf("recent = %+v", summary.Recent)
	}
}

func TestRatesIsSentWithoutTheKey(t *testing.T) {
	client, rec := fake(t, 200, `{"currency":"usd","model":"prepaid, cost-plus, capped",
		"egress_cost_per_gib":{"micros":80000,"display":"$0.08"},"markup_basis_points":2500,
		"platform_fee_per_gib":{"micros":10000,"display":"$0.01"},
		"ceiling_per_1000_participant_minutes":{"host":{"hd":{"micros":4000000,"display":"$4.00"}}}}`)
	rates, err := client.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.auth != "" {
		t.Errorf("rates sent Authorization %q to a public route", rec.auth)
	}
	if rates.MarkupBasisPoints != 2500 || rates.Ceiling["host"]["hd"].Micros != 4000000 {
		t.Errorf("rates = %+v", rates)
	}
}

func TestCheckoutPortalAndAutoTopUp(t *testing.T) {
	client, rec := fake(t, 200, `{"checkout_url":"https://checkout.stripe.test/cs_1"}`)
	link, err := client.Checkout(context.Background(), 5000)
	if err != nil || link != "https://checkout.stripe.test/cs_1" {
		t.Fatalf("Checkout = %q, %v", link, err)
	}
	if rec.method != http.MethodPost || rec.path != "/v1/billing/checkout" || rec.body["amount_cents"] != 5000.0 {
		t.Errorf("request = %s %s %v", rec.method, rec.path, rec.body)
	}
	if rec.contentType != "application/json" {
		t.Errorf("Content-Type = %q", rec.contentType)
	}

	client, rec = fake(t, 200, `{"portal_url":"https://billing.stripe.test/p_1"}`)
	link, err = client.Portal(context.Background())
	if err != nil || link != "https://billing.stripe.test/p_1" || rec.path != "/v1/billing/portal" {
		t.Fatalf("Portal = %q, %v (%s)", link, err, rec.path)
	}
	if len(rec.body) != 0 {
		t.Errorf("portal body = %v, want {} (the server disallows unknown fields)", rec.body)
	}

	client, rec = fake(t, 200, `{"id":"acme","status":"active","auto_top_up":{"enabled":true,"below":{"micros":1000000,"display":"$1.00"},"amount":{"micros":2000000,"display":"$2.00"}}}`)
	tenant, err := client.SetAutoTopUp(context.Background(), AutoTopUpRequest{Enabled: true, BelowCents: 100, AmountCents: 200})
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPut || rec.path != "/v1/billing/auto-top-up" {
		t.Errorf("request = %s %s", rec.method, rec.path)
	}
	if rec.body["enabled"] != true || rec.body["below_cents"] != 100.0 || rec.body["amount_cents"] != 200.0 {
		t.Errorf("body = %v", rec.body)
	}
	if !tenant.AutoTopUp.Enabled {
		t.Errorf("tenant = %+v", tenant)
	}
}

func TestKeys(t *testing.T) {
	client, rec := fake(t, 200, `{"keys":[{"id":"0123456789abcdef","name":"default","kind":"backend","created_at":"2026-01-01T00:00:00Z"},
		{"id":"fedcba9876543210","name":"old","kind":"relay","created_at":"2026-01-01T00:00:00Z","revoked_at":"2026-02-01T00:00:00Z"}]}`)
	keys, err := client.ListKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/v1/keys" || len(keys) != 2 || keys[0].RevokedAt != nil || keys[1].RevokedAt == nil {
		t.Errorf("keys = %+v", keys)
	}

	client, rec = fake(t, 201, `{"key":{"id":"aaaaaaaaaaaaaaaa","name":"ci","kind":"backend","created_at":"2026-01-01T00:00:00Z"},"api_key":"mqk_aaaaaaaaaaaaaaaa_xyz"}`)
	issued, err := client.IssueKey(context.Background(), "ci", KindBackend)
	if err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodPost || rec.body["name"] != "ci" || rec.body["kind"] != "backend" {
		t.Errorf("request = %s %v", rec.method, rec.body)
	}
	if issued.APIKey != "mqk_aaaaaaaaaaaaaaaa_xyz" || issued.Key.ID != "aaaaaaaaaaaaaaaa" {
		t.Errorf("issued = %+v", issued)
	}

	client, rec = fake(t, 204, ``)
	if err := client.RevokeKey(context.Background(), "a/b"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/v1/keys/a/b" {
		// PathEscape keeps the id one segment on the wire; the decoded path is what the fake sees.
		t.Errorf("request = %s %s", rec.method, rec.path)
	}
	if err := client.RevokeKey(context.Background(), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("RevokeKey(\"\") = %v, want ErrInvalid", err)
	}
}

func TestErrorsMapToSentinels(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{400, ErrInvalid},
		{401, ErrUnauthorized},
		{402, ErrPaymentRequired},
		{403, ErrForbidden},
		{404, ErrNotFound},
		{409, ErrConflict},
		{429, ErrRateLimited},
		{502, ErrPaymentProvider},
		{503, ErrUnavailable},
	}
	for _, c := range cases {
		client, _ := fake(t, c.status, `{"error":"something specific"}`)
		_, err := client.Summary(context.Background())
		if !errors.Is(err, c.want) {
			t.Errorf("HTTP %d: err = %v, want %v", c.status, err, c.want)
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != c.status || apiErr.Message != "something specific" {
			t.Errorf("HTTP %d: APIError = %+v", c.status, apiErr)
		}
	}

	client, _ := fake(t, 500, `{"error":"internal error"}`)
	_, err := client.Summary(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Errorf("HTTP 500: %v", err)
	}
}

func TestErrorDoesNotLeakKey(t *testing.T) {
	client, _ := fake(t, 401, `{"error":"invalid api key"}`)
	_, err := client.ListKeys(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if msg := err.Error(); contains(msg, "secret") || contains(msg, testKey) {
		t.Errorf("error leaks the key: %q", msg)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
