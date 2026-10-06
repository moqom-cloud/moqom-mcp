# Contributing

Thanks for wanting to help. moqom-mcp is Apache-2.0 and maintained by
Elliott Wave LLC. Issues and pull requests are welcome.

## Before you open a pull request

1. **Sign the CLA.** The first pull request you open asks you to sign the
   [Copyright Assignment Agreement](.github/CLA.md) by leaving a comment.
   Elliott Wave LLC owns all of MOQOM's intellectual property, open source
   included, so your contribution's copyright is assigned to it; you get a
   permanent licence back to use your own work for anything. We have contacted
   legal counsel to review the agreement.
2. **Open an issue first for anything bigger than a bug fix**, so nobody spends a
   weekend on a change that conflicts with work in progress.
3. **Tools are an API.** Agents and saved prompts depend on tool names and
   argument names. Additions are easy; renames and removals need a deprecation
   period and a changelog note.
4. **Destructive tools need `confirm`.** Any new tool that ends sessions,
   deletes, revokes, or charges money must be annotated `destructiveHint: true`
   (or otherwise gated) and refuse to act unless called with `confirm: true`.

## Building and testing

`go.mod` replaces `github.com/moqom-cloud/moqom-go` with `../moqom-go` until the
SDK has a published tag, so clone both repositories side by side:

```sh
git clone https://github.com/moqom-cloud/moqom-go
git clone https://github.com/moqom-cloud/moqom-mcp
cd moqom-mcp
go build ./...
go vet ./...
go test ./...
```

Tests use `httptest` fakes for the billing API and docs site and an in-memory MCP
client/server pair, so no MOQOM account, API key, or network access is needed.

## Security

Do not open a public issue for a vulnerability. Email security@moqom.cloud; see
[moqom.dev/security/disclosure](https://moqom.dev/security/disclosure/).

## Conduct

Be kind, be specific, assume good faith. Harassment of any kind gets you removed
from the project.
