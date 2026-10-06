// Package billing is a small typed client for MOQOM's billing and keys REST API.
//
// It covers the routes a tenant's backend key may call:
//
//	GET    /v1/rates                public rate card
//	GET    /v1/billing              balance, status, recent ledger
//	POST   /v1/billing/checkout     open a top-up
//	POST   /v1/billing/portal       Stripe customer portal
//	PUT    /v1/billing/auto-top-up  configure automatic top-ups
//	GET    /v1/keys                 list keys
//	POST   /v1/keys                 issue a key, shown once
//	DELETE /v1/keys/{id}            revoke a key
package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors returned by the API, matched with errors.Is against an *APIError.
var (
	ErrInvalid         = errors.New("invalid request")        // 400
	ErrUnauthorized    = errors.New("invalid api key")        // 401
	ErrPaymentRequired = errors.New("payment required")       // 402
	ErrForbidden       = errors.New("tenant suspended")       // 403
	ErrNotFound        = errors.New("not found")              // 404
	ErrConflict        = errors.New("conflict")               // 409
	ErrRateLimited     = errors.New("rate limited")           // 429
	ErrPaymentProvider = errors.New("payment provider error") // 502
	ErrUnavailable     = errors.New("payments unavailable")   // 503
)

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("moqom billing: HTTP %d", e.Status)
	}
	return fmt.Sprintf("moqom billing: HTTP %d: %s", e.Status, e.Message)
}

// Unwrap maps the status to a sentinel so callers can use errors.Is.
func (e *APIError) Unwrap() error {
	switch e.Status {
	case http.StatusBadRequest:
		return ErrInvalid
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusPaymentRequired:
		return ErrPaymentRequired
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusBadGateway:
		return ErrPaymentProvider
	case http.StatusServiceUnavailable:
		return ErrUnavailable
	}
	return nil
}

// Money is an amount in micro-dollars with its display form.
type Money struct {
	Micros  int64  `json:"micros"`
	Display string `json:"display"`
}

// AutoTopUp is the tenant's automatic top-up setting.
type AutoTopUp struct {
	Enabled bool  `json:"enabled"`
	Below   Money `json:"below"`
	Amount  Money `json:"amount"`
}

// Tenant is the account a key belongs to.
type Tenant struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	Status         string    `json:"status"`
	HasCard        bool      `json:"has_card"`
	AutoTopUp      AutoTopUp `json:"auto_top_up"`
	LastTopUpError string    `json:"last_top_up_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// Entry is one ledger line.
type Entry struct {
	Kind      string    `json:"kind"`
	Amount    Money     `json:"amount"`
	Capped    bool      `json:"capped,omitempty"`
	Reference string    `json:"reference,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at"`
}

// Summary is GET /v1/billing.
type Summary struct {
	Tenant   Tenant  `json:"tenant"`
	Balance  Money   `json:"balance"`
	Admitted bool    `json:"admitted"`
	Recent   []Entry `json:"recent"`
}

// Rates is GET /v1/rates.
type Rates struct {
	Currency          string `json:"currency"`
	Model             string `json:"model"`
	EgressCostPerGiB  Money  `json:"egress_cost_per_gib"`
	MarkupBasisPoints int64  `json:"markup_basis_points"`
	PlatformFeePerGiB Money  `json:"platform_fee_per_gib"`
	// Ceiling is role -> tier -> price per 1,000 participant-minutes.
	Ceiling map[string]map[string]Money `json:"ceiling_per_1000_participant_minutes"`
}

// AutoTopUpRequest is PUT /v1/billing/auto-top-up.
type AutoTopUpRequest struct {
	Enabled     bool  `json:"enabled"`
	BelowCents  int64 `json:"below_cents"`
	AmountCents int64 `json:"amount_cents"`
}

// Key is an API key, without its secret.
type Key struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// IssuedKey is POST /v1/keys: the record plus the plaintext, shown once.
type IssuedKey struct {
	Key    Key    `json:"key"`
	APIKey string `json:"api_key"`
}

// Key kinds accepted by IssueKey.
const (
	KindBackend = "backend"
	KindRelay   = "relay"
)

// Client calls the billing API with one backend key.
type Client struct {
	base       *url.URL
	key        string
	httpClient *http.Client
	userAgent  string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New builds a client for baseURL, authenticating with key.
func New(baseURL, key string, options ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("moqom billing: base URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("moqom billing: base URL %q must be http(s)", baseURL)
	}
	c := &Client{
		base:       u,
		key:        key,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		userAgent:  "moqom-mcp",
	}
	for _, option := range options {
		option(c)
	}
	return c, nil
}

// Rates reads the public rate card. It is sent without the key, since the
// route is public and the key has no business travelling where it is not needed.
func (c *Client) Rates(ctx context.Context) (Rates, error) {
	var out Rates
	err := c.do(ctx, http.MethodGet, "/v1/rates", nil, &out, false)
	return out, err
}

// Summary reads balance, status, admission, and recent ledger entries.
func (c *Client) Summary(ctx context.Context) (Summary, error) {
	var out Summary
	err := c.do(ctx, http.MethodGet, "/v1/billing", nil, &out, true)
	if out.Recent == nil {
		out.Recent = []Entry{}
	}
	return out, err
}

// Checkout opens a Stripe Checkout session for a top-up and returns its URL.
func (c *Client) Checkout(ctx context.Context, amountCents int64) (string, error) {
	var out struct {
		URL string `json:"checkout_url"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/billing/checkout", map[string]int64{"amount_cents": amountCents}, &out, true)
	return out.URL, err
}

// Portal opens a Stripe customer portal session and returns its URL.
func (c *Client) Portal(ctx context.Context) (string, error) {
	var out struct {
		URL string `json:"portal_url"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/billing/portal", struct{}{}, &out, true)
	return out.URL, err
}

// SetAutoTopUp configures automatic top-ups and returns the updated tenant.
func (c *Client) SetAutoTopUp(ctx context.Context, request AutoTopUpRequest) (Tenant, error) {
	var out Tenant
	err := c.do(ctx, http.MethodPut, "/v1/billing/auto-top-up", request, &out, true)
	return out, err
}

// ListKeys lists the tenant's keys, including revoked ones.
func (c *Client) ListKeys(ctx context.Context) ([]Key, error) {
	var out struct {
		Keys []Key `json:"keys"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/keys", nil, &out, true)
	if out.Keys == nil {
		out.Keys = []Key{}
	}
	return out.Keys, err
}

// IssueKey creates a key. The plaintext in the result is never shown again.
func (c *Client) IssueKey(ctx context.Context, name, kind string) (IssuedKey, error) {
	var out IssuedKey
	body := map[string]string{"name": name}
	if kind != "" {
		body["kind"] = kind
	}
	err := c.do(ctx, http.MethodPost, "/v1/keys", body, &out, true)
	return out, err
}

// RevokeKey revokes a key by ID.
func (c *Client) RevokeKey(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("%w: key id is required", ErrInvalid)
	}
	return c.do(ctx, http.MethodDelete, "/v1/keys/"+url.PathEscape(id), nil, nil, true)
}

const largestResponse = 4 << 20

func (c *Client) do(ctx context.Context, method, path string, body, out any, authenticated bool) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("moqom billing: encoding request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, reader)
	if err != nil {
		return fmt.Errorf("moqom billing: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+c.key)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("moqom billing: %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, largestResponse))
	if err != nil {
		return fmt.Errorf("moqom billing: reading response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode > 299 {
		apiErr := &APIError{Status: response.StatusCode}
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil {
			apiErr.Message = envelope.Error
		}
		return apiErr
	}
	if out == nil || response.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("moqom billing: decoding %s %s: %w", method, path, err)
	}
	return nil
}
