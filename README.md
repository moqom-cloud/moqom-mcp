# moqom-mcp

A [Model Context Protocol](https://modelcontextprotocol.io) server that lets your
AI agents operate your [MOQOM](https://moqom.cloud) tenant: create and close
rooms, mint join tokens, moderate participants, check your balance and top up,
manage API keys, and search the docs at [moqom.dev](https://moqom.dev).

It runs on your machine (or your infrastructure) with your own API key. It speaks
MCP over **stdio** for desktop clients such as Cursor, Claude Desktop, and VS Code,
or over **Streamable HTTP** behind a bearer token.

Built on the official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk)
and the [MOQOM Go SDK](https://github.com/moqom-cloud/moqom-go).

## Install

```sh
go install github.com/moqom-cloud/moqom-mcp@latest
```

> `go install …@latest` works once `moqom-go` has a published tag and the
> `replace` directive in `go.mod` is removed; until then, build from a checkout
> (see [CONTRIBUTING.md](CONTRIBUTING.md)).

**Prebuilt binaries** for macOS, Linux, and Windows (amd64 and arm64) are attached
to each [GitHub release](https://github.com/moqom-cloud/moqom-mcp/releases), built
with GoReleaser from [`.goreleaser.yaml`](.goreleaser.yaml).

**Homebrew** (coming soon; the tap is not published yet):

```sh
brew install moqom-cloud/tap/moqom-mcp
```

## Configuration

| Variable            | Required | Default                   | Purpose                                                   |
| ------------------- | -------- | ------------------------- | --------------------------------------------------------- |
| `MOQOM_API_KEY`     | yes      |                           | A **backend** API key, `mqk_…`                            |
| `MOQOM_API_URL`     | no       | `https://api.moqom.cloud` | Billing and keys REST API                                 |
| `MOQOM_CONTROL_URL` | no       | `https://api.moqom.cloud` | gRPC admin endpoint (rooms, tokens, moderation)           |
| `MOQOM_TENANT`      | no       | looked up from the key    | Tenant ID; skips one lookup on first use                  |
| `MOQOM_DOCS_URL`    | no       | `https://moqom.dev`       | Docs site for `search_docs` and `get_doc_page`            |
| `MOQOM_MCP_TOKEN`   | HTTP     |                           | Bearer token HTTP clients must send (`--http` mode only)  |

The API key is never logged; logs go to stderr and name the key only by its
public ID.

### Cursor

`.cursor/mcp.json` in your project (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "moqom": {
      "command": "moqom-mcp",
      "env": {
        "MOQOM_API_KEY": "${env:MOQOM_API_KEY}"
      }
    }
  }
}
```

### Claude Desktop

`claude_desktop_config.json` (Settings → Developer → Edit Config):

```json
{
  "mcpServers": {
    "moqom": {
      "command": "/absolute/path/to/moqom-mcp",
      "env": {
        "MOQOM_API_KEY": "mqk_..."
      }
    }
  }
}
```

Claude Desktop does not inherit your shell's `PATH`, so give the absolute path
(`which moqom-mcp`, usually `~/go/bin/moqom-mcp`).

### VS Code

`.vscode/mcp.json`, prompting for the key once and storing it securely:

```json
{
  "inputs": [
    {
      "type": "promptString",
      "id": "moqom-api-key",
      "description": "MOQOM backend API key (mqk_...)",
      "password": true
    }
  ],
  "servers": {
    "moqom": {
      "type": "stdio",
      "command": "moqom-mcp",
      "env": {
        "MOQOM_API_KEY": "${input:moqom-api-key}"
      }
    }
  }
}
```

### Streamable HTTP

```sh
export MOQOM_API_KEY=mqk_...
export MOQOM_MCP_TOKEN="$(openssl rand -hex 32)"
moqom-mcp --http :8765
# serving on http://127.0.0.1:8765/mcp
```

Clients must send `Authorization: Bearer $MOQOM_MCP_TOKEN`. An address without a
host binds to `127.0.0.1`; pass an explicit host (for example `0.0.0.0:8765`) to
listen beyond loopback, and put TLS in front of it if you do.

## Tools

| Area       | Tool                   | Notes                                                                 |
| ---------- | ---------------------- | --------------------------------------------------------------------- |
| Rooms      | `list_rooms`           | Filter by lifecycle; paginated                                         |
|            | `get_room`             |                                                                        |
|            | `create_room`          | Policy: age gate, recording, residency, caps; optional `starts_at`     |
|            | `close_room`           | **Destructive**, needs `confirm: true`                                 |
| Tokens     | `mint_join_token`      | Username, device thumbprint, room, publish scopes, preset, owner. The token is a credential |
| Moderation | `list_participants`    |                                                                        |
|            | `kick_participant`     | They may rejoin                                                        |
|            | `ban_participant`      | **Destructive**, needs `confirm: true`; account, device, or IP prefix; room or tenant scope |
|            | `unban`                |                                                                        |
|            | `force_mute`, `unmute` | Scope `everyone` or `audience`                                         |
| Billing    | `get_balance`          | Balance, status, admitted, auto top-up, recent ledger                  |
|            | `get_rates`            | Public rate card                                                       |
|            | `create_checkout_link` | Stripe Checkout for `amount_cents`; nothing is charged until paid      |
|            | `set_auto_top_up`      | Enabling needs `confirm: true` (it authorises future charges)          |
|            | `open_billing_portal`  | Stripe customer portal link                                            |
| Keys       | `list_api_keys`        | Never returns secrets                                                  |
|            | `issue_api_key`        | Plaintext returned **once**; store it immediately                      |
|            | `revoke_api_key`       | **Destructive**, needs `confirm: true`; refuses the server's own key   |
| Docs       | `search_docs`          | Ranked keyword search over `llms-full.txt`, cached 10 minutes          |
|            | `get_doc_page`         | One docs page as text; same origin only                                |

Moderation results include `enforced`. When it is `false`, the action is recorded
but a relay has not confirmed it yet, and the agent is told to say so.

**Resources:** `moqom://rates` and `moqom://billing/summary`.

**Prompts:** `set-up-a-live-room` walks an agent through checking the balance,
creating a room, minting the host's token, and building the audience link from
your app's watch-page URL.

## Security notes

- **Use a dedicated backend key.** Issue one key per agent setup (`issue_api_key`
  or the console) so you can revoke it alone. A backend key is the owner of your
  tenant: it can do everything these tools do, including issuing more keys.
  Relay keys cannot be used here.
- **Destructive tools need `confirm: true`.** `close_room`, `ban_participant`,
  and `revoke_api_key` are annotated `destructiveHint: true` and refuse to act
  without an explicit `confirm`, as does enabling auto top-up. Keep your client's
  per-tool approval prompts on for them.
- **Tokens and keys are credentials.** `mint_join_token` and `issue_api_key`
  return secrets into the agent's context. Do not run this server in an agent
  whose transcripts are shared or logged somewhere you would not paste a password.
- **HTTP mode** requires `MOQOM_MCP_TOKEN`, binds to loopback by default, and
  rejects DNS-rebinding requests to localhost.
- `get_doc_page` fetches only from `MOQOM_DOCS_URL`'s origin and refuses
  cross-origin redirects, so it cannot be used as an open proxy.

## Contribute

Bug reports, fixes, and new tools are welcome. Read
[CONTRIBUTING.md](CONTRIBUTING.md): the first pull request asks you to sign the
[CLA](.github/CLA.md), and `go test ./...` runs with no account or network.

## Donate

moqom-mcp is free. If it saves you time, you can support the open-source work at
[elliottwave.dev/donate](https://elliottwave.dev/donate/).

## Security

Report vulnerabilities privately to **security@moqom.cloud**, not in a public
issue. See [moqom.dev/security/disclosure](https://moqom.dev/security/disclosure/).

Bug bounty: [hackerone.com/elliottwave](https://hackerone.com/elliottwave)
(TODO: not live yet; use email until it is).

## Licence

Apache-2.0. © 2026 Elliott Wave LLC. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
MOQOM is a trademark of Elliott Wave LLC. Patents pending.
