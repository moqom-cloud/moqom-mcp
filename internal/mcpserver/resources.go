package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moqom-cloud/moqom-mcp/internal/billing"
)

const (
	ratesURI          = "moqom://rates"
	billingSummaryURI = "moqom://billing/summary"
)

func registerResources(server *mcp.Server, client *billing.Client) {
	server.AddResource(&mcp.Resource{
		URI:         ratesURI,
		Name:        "rates",
		Title:       "MOQOM rate card",
		Description: "Public pricing: egress cost, markup, platform fee, and per-tier price ceilings.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		rates, err := client.Rates(ctx)
		if err != nil {
			return nil, billingHint(err)
		}
		return jsonResource(ratesURI, rates)
	})

	server.AddResource(&mcp.Resource{
		URI:         billingSummaryURI,
		Name:        "billing-summary",
		Title:       "Billing summary",
		Description: "This tenant's balance, status, admission, auto top-up settings, and recent ledger entries.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		summary, err := client.Summary(ctx)
		if err != nil {
			return nil, billingHint(err)
		}
		return jsonResource(billingSummaryURI, summary)
	})
}

func jsonResource(uri string, value any) (*mcp.ReadResourceResult, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI: uri, MIMEType: "application/json", Text: string(encoded),
	}}}, nil
}
