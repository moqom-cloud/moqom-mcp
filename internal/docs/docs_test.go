package docs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const llmsFull = `# Top-ups & auto top-up
Source: https://moqom.dev/billing/top-ups/

MOQOM is prepaid. Add credit with a checkout link.

## Auto top-up

When the balance falls below a threshold, MOQOM charges the saved card a fixed amount.
Configure the threshold and amount with PUT /v1/billing/auto-top-up.

# Moderation
Source: https://moqom.dev/guides/moderation/

## Bans

A device ban survives a new account. An address ban is blunt.

` + "```" + `
# not a heading: inside a code fence
room.Ban(ctx, request)
` + "```" + `

# Untitled page without a source

Concepts about relays and rooms.
`

func TestParse(t *testing.T) {
	sections := Parse(llmsFull, "https://moqom.dev")
	byTitle := map[string]Section{}
	for _, s := range sections {
		byTitle[s.Title] = s
	}
	if got := byTitle["Top-ups & auto top-up"].URL; got != "https://moqom.dev/billing/top-ups/" {
		t.Errorf("page URL = %q", got)
	}
	if got := byTitle["Auto top-up"].URL; got != "https://moqom.dev/billing/top-ups/#auto-top-up" {
		t.Errorf("section URL = %q", got)
	}
	if got := byTitle["Auto top-up"].Page; got != "Top-ups & auto top-up" {
		t.Errorf("section page = %q", got)
	}
	if _, ok := byTitle["not a heading: inside a code fence"]; ok {
		t.Error("a line inside a code fence was parsed as a heading")
	}
	if !strings.Contains(byTitle["Bans"].Body, "room.Ban") {
		t.Errorf("fenced code missing from body: %q", byTitle["Bans"].Body)
	}
	if got := byTitle["Untitled page without a source"].URL; got != "https://moqom.dev/#untitled-page-without-a-source" {
		t.Errorf("fallback URL = %q", got)
	}
}

func TestRank(t *testing.T) {
	sections := Parse(llmsFull, "https://moqom.dev")
	results := Rank(sections, "auto top-up threshold", Terms("auto top-up threshold"), 3)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].URL != "https://moqom.dev/billing/top-ups/#auto-top-up" {
		t.Errorf("top result = %+v", results[0])
	}
	if !strings.Contains(results[0].Snippet, "threshold") {
		t.Errorf("snippet = %q", results[0].Snippet)
	}

	results = Rank(sections, "device ban", Terms("device ban"), 1)
	if len(results) != 1 || !strings.Contains(results[0].Title, "Bans") {
		t.Errorf("results = %+v", results)
	}

	if results := Rank(sections, "kubernetes", Terms("kubernetes"), 5); len(results) != 0 {
		t.Errorf("unrelated query matched: %+v", results)
	}
}

func TestTerms(t *testing.T) {
	got := Terms("How do I ban a DEVICE, and ban it again?")
	want := []string{"ban", "device", "again"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Terms = %v, want %v", got, want)
	}
}

func docsServer(t *testing.T, fetches *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/llms-full.txt", func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = io.WriteString(w, llmsFull)
	})
	mux.HandleFunc("/guides/moderation/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>Moderation | MOQOM Docs</title>
<script>var tracking = 1;</script><style>body{}</style></head>
<body><nav>Sidebar link</nav><main><h1>Moderation</h1><p>Kick is for a bad <em>moment</em>.</p>
<ul><li>Ban</li><li>Mute</li></ul><pre>room.Kick(ctx)</pre></main><footer>Footer</footer></body></html>`)
	})
	mux.HandleFunc("/escape", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.test/", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestSearchCachesForTenMinutes(t *testing.T) {
	var fetches atomic.Int32
	server := docsServer(t, &fetches)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	client.SetClock(func() time.Time { return now })

	for range 3 {
		results, err := client.Search(context.Background(), "auto top-up", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) == 0 {
			t.Fatal("no results")
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("fetched %d times within the TTL, want 1", n)
	}

	now = now.Add(CacheTTL + time.Second)
	if _, err := client.Search(context.Background(), "ban", 2); err != nil {
		t.Fatal(err)
	}
	if n := fetches.Load(); n != 2 {
		t.Errorf("fetched %d times after the TTL, want 2", n)
	}

	if _, err := client.Search(context.Background(), "the a of", 2); err == nil {
		t.Error("a query of only stopwords should be refused")
	}
}

func TestPage(t *testing.T) {
	var fetches atomic.Int32
	server := docsServer(t, &fetches)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	pageURL, title, text, err := client.Page(context.Background(), "guides/moderation/")
	if err != nil {
		t.Fatal(err)
	}
	if pageURL != server.URL+"/guides/moderation/" || title != "Moderation | MOQOM Docs" {
		t.Errorf("url, title = %q, %q", pageURL, title)
	}
	for _, want := range []string{"# Moderation", "Kick is for a bad moment.", "- Ban", "room.Kick(ctx)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"tracking", "Sidebar", "Footer"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("text contains %q:\n%s", unwanted, text)
		}
	}
}

func TestPageRefusesOtherOrigins(t *testing.T) {
	var fetches atomic.Int32
	server := docsServer(t, &fetches)
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"https://evil.test/guides/",
		"//evil.test/guides/",
		"http://169.254.169.254/latest/meta-data/",
		strings.Replace(server.URL, "http://", "https://", 1) + "/guides/",
	} {
		if _, _, _, err := client.Page(context.Background(), path); !errors.Is(err, ErrOffOrigin) {
			t.Errorf("Page(%q) = %v, want ErrOffOrigin", path, err)
		}
	}
	if _, _, _, err := client.Page(context.Background(), "/escape"); !errors.Is(err, ErrOffOrigin) {
		t.Errorf("cross-origin redirect = %v, want ErrOffOrigin", err)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Auto top-up":           "auto-top-up",
		"Top-ups & auto top-up": "top-ups--auto-top-up",
		"What is MOQOM?":        "what-is-moqom",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
