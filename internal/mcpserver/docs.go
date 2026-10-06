package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moqom-cloud/moqom-mcp/internal/docs"
)

type searchDocsIn struct {
	Query string `json:"query" jsonschema:"keywords to search for, e.g. 'auto top-up threshold' or 'device ban'"`
	Limit int    `json:"limit,omitempty" jsonschema:"how many results, 1 to 20; default 5"`
}

type searchDocsOut struct {
	Results []docs.Result `json:"results"`
}

type docPageIn struct {
	Path string `json:"path" jsonschema:"a path on the docs site such as /guides/backend-go/, or a full URL on the same origin"`
}

type docPageOut struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text"`
}

func (t *tools) registerDocs(server *mcp.Server) {
	maxResults := 20.0
	searchSchema := schema[searchDocsIn](nil, map[string]float64{"limit": 1})
	searchSchema.Properties["limit"].Maximum = &maxResults

	addTool(server, &mcp.Tool{
		Name: "search_docs",
		Description: "Search the MOQOM documentation (moqom.dev) by keyword. Returns the best-matching sections with URLs and snippets; " +
			"follow up with get_doc_page for the full text, and cite the URL in answers.",
		Annotations: &mcp.ToolAnnotations{Title: "Search docs", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: searchSchema,
	}, func(ctx context.Context, in searchDocsIn) (searchDocsOut, error) {
		if err := requireText("query", in.Query); err != nil {
			return searchDocsOut{}, err
		}
		limit := min(max(in.Limit, 0), 20)
		results, err := t.deps.Docs.Search(ctx, in.Query, limit)
		if results == nil {
			results = []docs.Result{}
		}
		return searchDocsOut{Results: results}, err
	})

	addTool(server, &mcp.Tool{
		Name:        "get_doc_page",
		Description: "Fetch one page of the MOQOM documentation as plain text. Only pages on the documentation site itself can be fetched.",
		Annotations: &mcp.ToolAnnotations{Title: "Get doc page", ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(true)},
	}, func(ctx context.Context, in docPageIn) (docPageOut, error) {
		pageURL, title, text, err := t.deps.Docs.Page(ctx, in.Path)
		return docPageOut{URL: pageURL, Title: title, Text: text}, err
	})
}
