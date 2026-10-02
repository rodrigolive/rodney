# AGENTS.md: rodney (Go)

rodrigolive fork of [simonw/rodney](https://github.com/simonw/rodney): a CLI that drives a persistent Chrome via [go-rod](https://github.com/go-rod/rod). Upstream is the `upstream` remote; see the README's fork section for what was merged from where.

## Workflow

- `make vet` after every change (go vet + gofmt check + compile tests); `make test` for the full suite; `make install` to replace `~/util/rodney`.
- `make test-sh` runs `test.sh` against `tests/testserver` in a throwaway `RODNEY_HOME`. Never run `./test.sh` bare: it starts and stops whatever session the default state dir points at.
- CI (`.github/workflows/ci.yml`) runs vet/gofmt/tidy, plus `go test` and `test.sh` on ubuntu and macos. `gh workflow run ci.yml` triggers it by hand.
- Releases: `make release TAG=rod-vX.Y.Z` once CI is green on the pushed HEAD; `publish.yml` then attaches the macOS/Linux binaries. Fork tags always carry the `rod-` prefix, because upstream owns `vX.Y.Z`. Its PyPI jobs only run on simonw/rodney.
- Tests are integration-style: `TestMain` boots one real headless Chrome against a shared `httptest` fixture server. Add a fixture route and a `Test…` that asserts on observed behaviour, not a mock.
- Keep `go.mod` to runtime dependencies (rod, gson) so upstream merges stay clean.
- Keep the module path `github.com/simonw/rodney` for the same reason.
- Merge third-party fork branches by pinned SHA, after reading every added line.

## State & concurrency model (multi-agent safety)

Rodney is driven by many agents at once. Preserve these invariants when touching state or tab code:

- **Per-agent isolation**: `--session NAME` / `RODNEY_SESSION` scopes state to `<base>/sessions/<name>/`, each with its own Chrome. Resolution lives in `stateDir()` (session) over `baseStateDir()` (`RODNEY_HOME` → `--local/--global` → `~/.rodney`).
- **Per-command tab targeting**: `--page <idx|substring>` (index into `orderedPages`, or a unique URL/title substring) wins over `--target ID` / `RODNEY_TARGET` (stable target id), which wins over the saved active tab. All of it is stripped in `main()` and honoured only in `getActivePage`, so new commands get targeting for free. Don't re-implement it.
- **Tabs are addressed by stable target id, never raw `browser.Pages()` order**: go through `orderedPages` (sorted by target id) and `matchTargetID` (exact, else unique prefix). Persist the active tab with `setActivePage` (sets `ActiveTarget`, with `ActivePage` as a legacy fallback).
- **`saveState` is atomic** (temp + rename). Keep it that way.
- **`start` is non-destructive**: it reuses a live session and only relaunches under `--replace`. Never `Close()` the browser on a reuse path; that kills Chrome.
- **Device emulation**: `connectBrowser` skips go-rod's default device for visible sessions (`--show`, `connect`). Headless sessions keep the 1280x800 device, with the UA pinned at start in `State.UserAgent`. Changing this changes responsive layouts for every headless caller.
- **Helper processes** (`_proxy`, `_console_logger`) re-exec the rodney binary and receive everything they need as arguments. The console sidecar pins its state dir to its log's directory, and `console.log` is 0600. Never signal a PID read from state.json without `processCommandContains` confirming it's still that helper (or Chrome with this session's `--user-data-dir`); go through `stopHelpers`.

## Layout & gotchas

- Flat single `main` package: commands are dispatched in `main()` and parsed with `flag.FlagSet`. `help.txt` is embedded via `//go:embed`. The version comes from `-X main.version` (see `Makefile`).
- Chrome launch flags live in `cmdStart`, plus helpers in `chrome_flags.go` (macOS skips `--single-process`; experimental field trials are off) and `extensions.go`.
- `fatal()` calls `os.Exit(2)`, which skips deferred calls. Clean up explicitly before calling it.
- Unknown commands get a `suggestCommand` did-you-mean over `commandNames`. Keep that list in sync with the dispatch switch.
