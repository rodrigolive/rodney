# CLAUDE.md — rodney (Go)

Personal fork of [simonw/rodney](https://github.com/simonw/rodney), not upstreamed — a CLI driving persistent headless Chrome via [go-rod](https://github.com/go-rod/rod).

## Workflow

- **`just ok` is the gate** — run after every change. Never call `go build` / `go test` / `gofmt` / `golangci-lint` directly; go through `justfile` recipes (add one if it's missing). Recipes run the dev tools pinned in go.mod's `tool` block via `go tool`, so nothing needs a global install.
- **TDD by default** (skip only for trivial or docs edits). Tests are integration-style: `TestMain` boots one real headless Chrome against a shared `httptest` fixture server — add a fixture route + a `Test…` that asserts on observed behaviour, not a unit mock.

## Skills — invoke BEFORE writing Go, not after

| Before you… | Invoke |
|---|---|
| write or edit any `.go` | `go-modern` — stdlib + 1.21–1.26 language idioms the linters expect |
| write any slice / map / set transform | `lo` — Map/Filter/Reduce/GroupBy, set ops, `*Err` variants |

`samber/lo` isn't a dependency yet — `go get github.com/samber/lo` the first time a transform needs it.

## Recipes

```
just ok          # gate: tidy + fmt + vet + lint + test + build + install
just install     # install to ~/go/bin as the system binary (version-stamped)
just run ARGS    # run from source, e.g. `just run open https://example.com`
just test        # full suite — TestMain boots one headless Chrome (~8s)
just test-sh     # bash integration harness (test.sh; needs a built binary)
```

## State & concurrency model (multi-agent safety)

Rodney is built to be driven by many agents at once. Two isolation models, plus
invariants you must preserve when touching state or tab code:

- **Per-agent isolation** — `--session NAME` / `RODNEY_SESSION` scopes state to
  `<base>/sessions/<name>/`, so each agent gets its own Chrome. Resolution lives
  in `stateDir()` (session) over `baseStateDir()` (`RODNEY_HOME` → `--local/--global` → `~/.rodney`).
- **Shared-browser per-tab pinning** — `--target ID` / `RODNEY_TARGET` makes a
  command act on a specific tab. Both globals are stripped in `main()` via
  `extractValueFlag` (env is the `cmp.Or` fallback) and honored centrally in
  `getActivePage`, so a new command gets targeting for free — don't re-implement it.
- **Tabs are addressed by stable target id, never raw `browser.Pages()` order**
  (Chrome's order isn't stable — new tabs can sort first). Always go through
  `orderedPages` (sorts by target id) and `matchTargetID` (exact, else unique
  prefix). The active tab is persisted as `ActiveTarget` (id), with `ActivePage`
  (index) only a legacy fallback; set both via `setActivePage`.
- **`saveState` is atomic** (temp + rename) so concurrent writers can't corrupt
  `state.json` — keep it that way.
- **`start` is non-destructive**: it reuses a live session and only kills/relaunches
  under `--replace`. Never call `browser.Close()`/`MustClose()` on a reuse path —
  that terminates Chrome; just let the process exit to drop the connection.

## Layout & gotchas

- Flat single `main` package (no `cmd/` / `internal/`): commands dispatched in `main()`, parsed with `flag.FlagSet` (no CLI framework). `help.txt` is embedded via `//go:embed`; version comes from `-X main.version`.
- Tests need Chrome — rod auto-downloads Chromium if none is found, or set `ROD_CHROME_BIN`.
- `fatal()` calls `os.Exit(2)`, but staticcheck doesn't know it's no-return: after a nil-guard that calls `fatal`, dereference the pointer *inside* the `!= nil` branch, not after it (see `cmdAttr`).
- Unknown commands get a `suggestCommand` did-you-mean (Levenshtein ≤2 over `commandNames`) instead of dumping help — keep `commandNames` in sync with the dispatch switch.
