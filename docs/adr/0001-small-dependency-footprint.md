# 1. A small dependency footprint

Status: accepted

## Context

The console holds the keys to every team's LLM spend. Each dependency is
code that runs with that access, and something to keep patched.

## Decision

- **Go: standard library only**, like the gateway. Routing (Go 1.22
  patterns), PBKDF2 (Go 1.24), HMAC cookies, JSON and the Prometheus HTTP
  API are all in the standard library.
- **Web: React, nothing else at runtime.** Charts are hand-written SVG,
  dialogs are the native `<dialog>` element, routing is three paths and
  `history.pushState`. esbuild bundles it; TypeScript checks it.
- The built app is embedded in the binary (`go:embed`), so a release is
  one file and the UI can never be out of step with the API.

## Consequences

- `govulncheck` covers the whole backend; the browser runs React and nothing else.
- Some things are more work than pulling in a library: the bar chart is
  about 80 lines. That is a fair trade at this size.
- If the UI grows (forms with many fields, client-side caching across
  pages), adding TanStack Query or a form library would be reasonable.
