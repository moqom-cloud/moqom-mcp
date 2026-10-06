package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/moqom-cloud/moqom-mcp/internal/billing"
)

// billingHint adds the next step an agent should take to a billing error.
func billingHint(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, billing.ErrUnauthorized):
		return fmt.Errorf("%w (MOQOM_API_KEY must be an unrevoked backend key)", err)
	case errors.Is(err, billing.ErrPaymentRequired):
		return fmt.Errorf("%w (the balance is exhausted; create_checkout_link opens a top-up)", err)
	case errors.Is(err, billing.ErrForbidden):
		return fmt.Errorf("%w (the tenant is suspended; open_billing_portal or contact support)", err)
	case errors.Is(err, billing.ErrUnavailable):
		return fmt.Errorf("%w (payments are temporarily unavailable; try again later)", err)
	}
	return err
}

type emptyIn struct{}

type checkoutIn struct {
	AmountCents int64 `json:"amount_cents" jsonschema:"top-up amount in US cents, e.g. 5000 for $50.00"`
}

type urlOut struct {
	URL  string `json:"url"`
	Note string `json:"note"`
}

type autoTopUpIn struct {
	Enabled     bool  `json:"enabled" jsonschema:"turn automatic top-ups on or off"`
	BelowCents  int64 `json:"below_cents,omitempty" jsonschema:"top up when the balance falls below this many US cents"`
	AmountCents int64 `json:"amount_cents,omitempty" jsonschema:"how many US cents to charge each time"`
	Confirm     bool  `json:"confirm,omitempty" jsonschema:"required (true) when enabling, because it authorises charges to the saved card without further approval"`
}

func (t *tools) registerBilling(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name: "get_balance",
		Description: "Read the tenant's prepaid balance, account status, whether new sessions are currently admitted, " +
			"auto top-up settings, and recent ledger entries. Amounts are given in micro-dollars plus a display string.",
		Annotations: titled(readOnly, "Get balance"),
	}, func(ctx context.Context, _ emptyIn) (billing.Summary, error) {
		summary, err := t.deps.Billing.Summary(ctx)
		return summary, billingHint(err)
	})

	addTool(server, &mcp.Tool{
		Name:        "get_rates",
		Description: "Read MOQOM's public rate card: egress cost, markup, platform fee, and the price ceiling per 1,000 participant-minutes by role and tier.",
		Annotations: titled(readOnly, "Get rates"),
	}, func(ctx context.Context, _ emptyIn) (billing.Rates, error) {
		rates, err := t.deps.Billing.Rates(ctx)
		return rates, billingHint(err)
	})

	addTool(server, &mcp.Tool{
		Name: "create_checkout_link",
		Description: "Create a Stripe Checkout link to top up the prepaid balance by amount_cents. Nothing is charged until the user completes " +
			"checkout in a browser; give them the link.",
		Annotations: titled(additive, "Create checkout link"),
		InputSchema: schema[checkoutIn](nil, map[string]float64{"amount_cents": 1}),
	}, func(ctx context.Context, in checkoutIn) (urlOut, error) {
		link, err := t.deps.Billing.Checkout(ctx, in.AmountCents)
		if err != nil {
			return urlOut{}, billingHint(err)
		}
		return urlOut{URL: link, Note: "Open this link to pay. It is single-use and expires; nothing is charged until checkout completes."}, nil
	})

	addTool(server, &mcp.Tool{
		Name: "set_auto_top_up",
		Description: "Configure automatic top-ups: when the balance falls below below_cents, charge amount_cents to the saved card. " +
			"Enabling authorises future charges without further approval, so it requires confirm: true after the user explicitly agrees to the amounts. " +
			"Disabling needs no confirmation.",
		Annotations: &mcp.ToolAnnotations{Title: "Set auto top-up", DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(true)},
		InputSchema: schema[autoTopUpIn](nil, map[string]float64{"below_cents": 0, "amount_cents": 0}),
	}, func(ctx context.Context, in autoTopUpIn) (billing.Tenant, error) {
		if in.Enabled && !in.Confirm {
			return billing.Tenant{}, errors.New(`not done: enabling auto top-up authorises charges to the saved card and needs "confirm": true. ` +
				`Tell the user the threshold and amount, get their explicit approval, then call again`)
		}
		tenant, err := t.deps.Billing.SetAutoTopUp(ctx, billing.AutoTopUpRequest{
			Enabled: in.Enabled, BelowCents: in.BelowCents, AmountCents: in.AmountCents,
		})
		return tenant, billingHint(err)
	})

	addTool(server, &mcp.Tool{
		Name:        "open_billing_portal",
		Description: "Create a Stripe customer portal link where the user can manage their card, invoices, and receipts.",
		Annotations: titled(additive, "Open billing portal"),
	}, func(ctx context.Context, _ emptyIn) (urlOut, error) {
		link, err := t.deps.Billing.Portal(ctx)
		if err != nil {
			return urlOut{}, billingHint(err)
		}
		return urlOut{URL: link, Note: "Short-lived link to the Stripe customer portal for this tenant; give it to the account owner only."}, nil
	})
}

type listKeysOut struct {
	Keys       []billing.Key `json:"keys"`
	CurrentKey string        `json:"current_key_id,omitempty"`
}

type issueKeyIn struct {
	Name string `json:"name" jsonschema:"a label for the key, 1 to 100 characters, e.g. the service that will use it"`
	Kind string `json:"kind,omitempty" jsonschema:"backend (default) administers the tenant; relay only reports usage from a self-hosted relay"`
}

type issueKeyOut struct {
	Key     billing.Key `json:"key"`
	APIKey  string      `json:"api_key"`
	Warning string      `json:"warning"`
}

type revokeKeyIn struct {
	ID      string `json:"id" jsonschema:"the key id from list_api_keys (not the secret)"`
	Confirm bool   `json:"confirm" jsonschema:"must be true; set only after the user explicitly approved revoking this key"`
}

type revokeKeyOut struct {
	Revoked string `json:"revoked"`
}

func (t *tools) registerKeys(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name:        "list_api_keys",
		Description: "List the tenant's API keys (ids, names, kinds, creation and revocation times). Secrets are never returned. current_key_id is the key this server uses.",
		Annotations: titled(readOnly, "List API keys"),
	}, func(ctx context.Context, _ emptyIn) (listKeysOut, error) {
		keys, err := t.deps.Billing.ListKeys(ctx)
		if err != nil {
			return listKeysOut{}, billingHint(err)
		}
		return listKeysOut{Keys: keys, CurrentKey: t.deps.CurrentKeyID}, nil
	})

	addTool(server, &mcp.Tool{
		Name: "issue_api_key",
		Description: "Issue a new API key. The plaintext api_key is returned ONCE and can never be shown again: tell the user to store it " +
			"immediately in a secret manager. It is a credential with full control of the tenant (backend) or usage reporting (relay); " +
			"never paste it into chat channels, tickets, code, or logs.",
		Annotations: titled(additive, "Issue API key"),
		InputSchema: schema[issueKeyIn](map[string][]any{"kind": strings2any(billing.KindBackend, billing.KindRelay)}, nil),
	}, func(ctx context.Context, in issueKeyIn) (issueKeyOut, error) {
		if err := requireText("name", in.Name); err != nil {
			return issueKeyOut{}, err
		}
		issued, err := t.deps.Billing.IssueKey(ctx, in.Name, in.Kind)
		if err != nil {
			return issueKeyOut{}, billingHint(err)
		}
		return issueKeyOut{
			Key:     issued.Key,
			APIKey:  issued.APIKey,
			Warning: "Shown once. Store it now in a secret manager; it cannot be recovered, only revoked and replaced.",
		}, nil
	})

	addTool(server, &mcp.Tool{
		Name: "revoke_api_key",
		Description: "DESTRUCTIVE: permanently revoke an API key by id. Anything still using it stops working immediately. " +
			"The key this server runs on cannot be revoked here. Requires confirm: true, which you may only set after the user explicitly approves.",
		Annotations: destructive("Revoke API key"),
	}, func(ctx context.Context, in revokeKeyIn) (revokeKeyOut, error) {
		if !in.Confirm {
			return revokeKeyOut{}, errNotConfirmed
		}
		if err := requireText("id", in.ID); err != nil {
			return revokeKeyOut{}, err
		}
		if in.ID == t.deps.CurrentKeyID {
			return revokeKeyOut{}, errors.New("refusing to revoke the key this server is using; issue a replacement, reconfigure MOQOM_API_KEY, then revoke this one")
		}
		if err := t.deps.Billing.RevokeKey(ctx, in.ID); err != nil {
			return revokeKeyOut{}, billingHint(err)
		}
		return revokeKeyOut{Revoked: in.ID}, nil
	})
}
