// Package docs searches and fetches MOQOM's public documentation.
//
// Search runs over llms-full.txt, the whole site as one plain-text file,
// fetched once and cached. Pages are fetched individually and reduced to text,
// and only from the documentation origin: a tool that fetched arbitrary URLs
// on a model's say-so would be an open proxy into whatever network the server
// runs on.
package docs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// CacheTTL is how long llms-full.txt is kept before it is fetched again.
const CacheTTL = 10 * time.Minute

const (
	largestIndex = 16 << 20
	largestPage  = 4 << 20
	// MaxPageChars bounds what get_doc_page returns, so one page cannot eat a
	// model's whole context window.
	MaxPageChars = 60_000
)

// ErrOffOrigin is a page outside the documentation origin.
var ErrOffOrigin = errors.New("only pages on the documentation origin can be fetched")

// Section is one searchable chunk of the documentation.
type Section struct {
	Title string
	// Page is the title of the page the section belongs to, when known.
	Page string
	URL  string
	Body string
}

// Result is one search hit.
type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
}

// Client fetches from one documentation origin.
type Client struct {
	base       *url.URL
	httpClient *http.Client
	now        func() time.Time

	mu       sync.Mutex
	sections []Section
	fetched  time.Time
}

// New builds a client for the documentation at baseURL.
func New(baseURL string, httpClient *http.Client) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("docs: base URL: %w", err)
	}
	if (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, fmt.Errorf("docs: base URL %q must be http(s)", baseURL)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	// Copy so the redirect policy does not leak into a client the caller shares.
	hc := *httpClient
	hc.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if !sameOrigin(base, request.URL) {
			return ErrOffOrigin
		}
		return nil
	}
	return &Client{base: base, httpClient: &hc, now: time.Now}, nil
}

// SetClock replaces the clock, for tests of the cache.
func (c *Client) SetClock(now func() time.Time) { c.now = now }

// Search ranks sections of llms-full.txt against query and returns the best.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 5
	}
	terms := Terms(query)
	if len(terms) == 0 {
		return nil, errors.New("docs: the query has no searchable words")
	}
	sections, err := c.index(ctx)
	if err != nil {
		return nil, err
	}
	return Rank(sections, query, terms, limit), nil
}

func (c *Client) index(ctx context.Context) ([]Section, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sections != nil && c.now().Sub(c.fetched) < CacheTTL {
		return c.sections, nil
	}
	text, _, err := c.get(ctx, c.base.String()+"/llms-full.txt", largestIndex)
	if err != nil {
		if c.sections != nil {
			// A stale index beats no answer while the site is briefly down.
			return c.sections, nil
		}
		return nil, err
	}
	c.sections = Parse(text, c.base.String())
	c.fetched = c.now()
	return c.sections, nil
}

// Page fetches one documentation page and returns its URL, title, and text.
//
// path may be a site-relative path ("/guides/backend-go/") or an absolute URL
// on the documentation origin; anything else is refused.
func (c *Client) Page(ctx context.Context, path string) (pageURL, title, text string, err error) {
	target, err := c.resolve(path)
	if err != nil {
		return "", "", "", err
	}
	body, contentType, err := c.get(ctx, target.String(), largestPage)
	if err != nil {
		return "", "", "", err
	}
	if strings.Contains(contentType, "html") || looksLikeHTML(body) {
		title, text = HTMLToText(body)
	} else {
		text = body
	}
	if len(text) > MaxPageChars {
		text = text[:MaxPageChars] + "\n\n[truncated]"
	}
	return target.String(), title, text, nil
}

func (c *Client) resolve(path string) (*url.URL, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("docs: %w", err)
	}
	if ref.Scheme == "" && ref.Host == "" && !strings.HasPrefix(ref.Path, "/") {
		ref.Path = "/" + ref.Path
	}
	target := c.base.ResolveReference(ref)
	if !sameOrigin(c.base, target) {
		return nil, ErrOffOrigin
	}
	target.Fragment = ""
	return target, nil
}

func (c *Client) get(ctx context.Context, target string, limit int64) (string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", "", fmt.Errorf("docs: %w", err)
	}
	request.Header.Set("User-Agent", "moqom-mcp")
	request.Header.Set("Accept", "text/plain, text/markdown, text/html;q=0.9, */*;q=0.1")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("docs: fetching %s: %w", target, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("docs: fetching %s: HTTP %d", target, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return "", "", fmt.Errorf("docs: reading %s: %w", target, err)
	}
	return string(body), response.Header.Get("Content-Type"), nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

var (
	headingPattern = regexp.MustCompile(`^(#{1,3})\s+(.+?)\s*#*\s*$`)
	sourcePattern  = regexp.MustCompile(`(?i)^\s*(?:source|url):\s*(https?://\S+)\s*$`)
)

// Parse splits llms-full.txt into sections at headings of level one to three.
//
// A "Source: <url>" or "URL: <url>" line after a top-level heading names that
// page's address, as several llms.txt generators emit. Without one, a section
// links to the documentation root with a slug of its heading as the fragment,
// which is a best guess rather than a promise.
func Parse(text, base string) []Section {
	base = strings.TrimRight(base, "/")
	var (
		sections []Section
		current  *Section
		page     string
		pageURL  string
		body     strings.Builder
		inFence  bool
	)
	flush := func() {
		if current == nil {
			return
		}
		current.Body = strings.TrimSpace(body.String())
		if current.Body != "" || current.Title != "" {
			sections = append(sections, *current)
		}
		body.Reset()
		current = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence {
			if m := headingPattern.FindStringSubmatch(line); m != nil {
				flush()
				title := strings.TrimSpace(m[2])
				if len(m[1]) == 1 {
					page, pageURL = title, ""
				}
				sectionURL := pageURL
				if sectionURL == "" {
					sectionURL = base + "/#" + Slug(title)
				} else if len(m[1]) > 1 {
					sectionURL = pageURL + "#" + Slug(title)
				}
				current = &Section{Title: title, Page: page, URL: sectionURL}
				continue
			}
			if m := sourcePattern.FindStringSubmatch(line); m != nil && current != nil && strings.TrimSpace(body.String()) == "" {
				pageURL = m[1]
				if current.Title == page {
					current.URL = pageURL
				}
				continue
			}
		}
		if current == nil {
			current = &Section{Title: "", URL: base + "/"}
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return sections
}

var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true, "by": true,
	"can": true, "do": true, "does": true, "for": true, "from": true, "how": true, "i": true, "in": true,
	"is": true, "it": true, "of": true, "on": true, "or": true, "the": true, "to": true, "what": true,
	"when": true, "with": true, "you": true, "my": true, "me": true, "we": true, "this": true, "that": true,
}

// Terms lowercases and tokenizes a query, dropping stopwords and duplicates.
func Terms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, word := range tokenize(query) {
		if len(word) < 2 || stopwords[word] || seen[word] {
			continue
		}
		seen[word] = true
		terms = append(terms, word)
	}
	return terms
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	})
}

// Rank scores sections by term frequency, weighting title hits and rewarding
// sections that contain every term or the query as a phrase.
func Rank(sections []Section, query string, terms []string, limit int) []Result {
	phrase := strings.ToLower(strings.TrimSpace(query))
	type scored struct {
		index int
		score float64
	}
	var hits []scored
	for i, section := range sections {
		title := strings.ToLower(section.Title + " " + section.Page)
		bodyWords := tokenize(section.Body)
		counts := make(map[string]int, len(terms))
		for _, word := range bodyWords {
			for _, term := range terms {
				if word == term || (len(term) >= 4 && strings.HasPrefix(word, term)) {
					counts[term]++
				}
			}
		}
		var score float64
		matched := 0
		for _, term := range terms {
			termScore := 0.0
			if strings.Contains(title, term) {
				termScore += 5
			}
			if n := counts[term]; n > 0 {
				termScore += 1 + math.Log(float64(n))
			}
			if termScore > 0 {
				matched++
			}
			score += termScore
		}
		if score == 0 {
			continue
		}
		if matched == len(terms) && len(terms) > 1 {
			score *= 1.5
		}
		if len(terms) > 1 && strings.Contains(strings.ToLower(section.Body), phrase) {
			score += 4
		}
		// Long sections match more by being long; damp that.
		score /= 1 + math.Log1p(float64(len(bodyWords))/400)
		hits = append(hits, scored{i, score})
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	results := make([]Result, 0, len(hits))
	for _, hit := range hits {
		section := sections[hit.index]
		title := section.Title
		if section.Page != "" && section.Page != section.Title {
			title = section.Page + " › " + section.Title
		}
		results = append(results, Result{
			Title:   title,
			URL:     section.URL,
			Score:   math.Round(hit.score*100) / 100,
			Snippet: snippet(section.Body, terms, 500),
		})
	}
	return results
}

func snippet(body string, terms []string, width int) string {
	lower := strings.ToLower(body)
	start := -1
	for _, term := range terms {
		if i := strings.Index(lower, term); i >= 0 && (start < 0 || i < start) {
			start = i
		}
	}
	if start < 0 {
		start = 0
	}
	start = max(0, start-width/4)
	for start > 0 && !utf8Boundary(body, start) {
		start--
	}
	end := min(len(body), start+width)
	for end < len(body) && !utf8Boundary(body, end) {
		end++
	}
	out := strings.Join(strings.Fields(body[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(body) {
		out += "…"
	}
	return out
}

func utf8Boundary(s string, i int) bool { return i >= len(s) || s[i]&0xC0 != 0x80 }

// Slug follows github-slugger, which Starlight uses for heading anchors:
// lowercase, drop punctuation, and turn each space into a hyphen.
func Slug(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

func looksLikeHTML(body string) bool {
	head := strings.ToLower(strings.TrimSpace(body[:min(len(body), 512)]))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// HTMLToText reduces a page to its title and readable text, preferring the
// <main> element so navigation and footers are left out.
func HTMLToText(page string) (title, text string) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return "", page
	}
	var find func(*html.Node, string) *html.Node
	find = func(n *html.Node, tag string) *html.Node {
		if n.Type == html.ElementNode && n.Data == tag {
			return n
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if found := find(child, tag); found != nil {
				return found
			}
		}
		return nil
	}
	if t := find(doc, "title"); t != nil && t.FirstChild != nil {
		title = strings.TrimSpace(t.FirstChild.Data)
	}
	root := find(doc, "main")
	if root == nil {
		root = find(doc, "body")
	}
	if root == nil {
		root = doc
	}

	var b strings.Builder
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, pre bool) {
		switch n.Type {
		case html.TextNode:
			if pre {
				b.WriteString(n.Data)
				return
			}
			s := strings.Join(strings.Fields(n.Data), " ")
			atBreak := b.Len() == 0 || strings.HasSuffix(b.String(), "\n") || strings.HasSuffix(b.String(), " ")
			if n.Data != "" && unicode.IsSpace(rune(n.Data[0])) && !atBreak {
				b.WriteByte(' ')
			}
			b.WriteString(s)
			if s != "" && unicode.IsSpace(rune(n.Data[len(n.Data)-1])) {
				b.WriteByte(' ')
			}
			return
		case html.ElementNode:
			switch n.Data {
			case "script", "style", "noscript", "svg", "nav", "template", "button":
				return
			case "pre":
				b.WriteString("\n```\n")
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					walk(child, true)
				}
				b.WriteString("\n```\n")
				return
			case "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteString("\n\n" + strings.Repeat("#", int(n.Data[1]-'0')) + " ")
			case "li":
				b.WriteString("\n- ")
			case "br":
				b.WriteByte('\n')
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, pre)
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "p", "div", "section", "article", "table", "tr", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote":
				b.WriteString("\n")
			}
		}
	}
	walk(root, false)
	lines := strings.Split(b.String(), "\n")
	var out []string
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return title, strings.TrimSpace(strings.Join(out, "\n"))
}
