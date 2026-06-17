# CLAUDE.md — rodney (Go)

Personal fork of [simonw/rodney](https://github.com/simonw/rodney) — a CLI that
drives a persistent headless Chrome via [go-rod](https://github.com/go-rod/rod).

## Workflow

- **Audience: Python/JS developers learning Go.** Explicit over clever, readable
  over terse.
- **`just ok` is the gate** — run after every change. Don't call `go build` /
  `go test` / `gofmt` / `golangci-lint` directly; go through `justfile` recipes.
  Need a recipe that doesn't exist? Add it.
- **TDD by default.** Write the failing test first, then make it pass. Tests are
  integration-style here (`TestMain` drives real Chrome against an `httptest`
  fixture server) — add a fixture route + a `Test…` that asserts on observed
  behaviour. Skip TDD only for trivial edits (typos, docs, one-line refactors).
- **All work on `main`.** Commit only after explicit approval.
- Run `just bootstrap` once after cloning to install the dev tools pinned in
  go.mod's `tool` block (golangci-lint, goimports, gofumpt, dlv). Recipes invoke
  them via `go tool …`, so the pinned versions are used with or without bootstrap.

## Skills — invoke BEFORE the work, not after

Load the skill before you start, so you write it right the first time instead of
leaning on the linters to catch legacy patterns afterward.

| Before you… | Invoke |
|---|---|
| write or edit **any** Go (`.go`) | `go-modern` — stdlib + language idioms (1.21–1.26); use the modern forms, not the legacy ones the linters ban |
| write any slice / map / set transform | `lo` — `Map`/`Filter`/`Reduce`/`GroupBy`/`KeyBy`, set ops, `*Err` variants (replaces manual `for`+`append` loops) |

`go-modern` owns stdlib/language; `lo` owns functional transforms — they hand off
cleanly. `samber/lo` isn't a dependency yet; `go get github.com/samber/lo` the
first time a transform calls for it.

## Recipes

```
just ok          # gate: tidy + fmt + vet + lint + test + build + install
just install     # install rodney into ~/go/bin as the system binary (version-stamped)
just run ARGS    # run from source, e.g. `just run open https://example.com`
just test        # full suite (TestMain boots one headless Chrome, ~8s)
just test-race   # race detector
just test-sh     # bash integration harness (test.sh; needs a built binary)
just lint        # golangci-lint (.golangci.yml: standard + dupl + dupword)
just fmt         # gofmt + goimports
```

## Layout

Flat single `main` package:
- `main.go` — all commands; dispatch in `main()`, arg parsing via `flag.FlagSet`.
- `main_test.go` — Go unit + integration tests; `TestMain` launches a real
  headless Chrome and an `httptest` fixture server shared across tests.
- `proc_unix.go` / `proc_windows.go` — build-tagged process helpers.
- `help.txt` — CLI help, embedded via `//go:embed`.
- `version` (`main.go`) is stamped at build time by `just build` (`-X main.version`).

## Notes

- Tests need Chrome: rod auto-downloads Chromium if none is found, or set
  `ROD_CHROME_BIN`. CI (`.github/workflows/check.yml`) installs it via
  `browser-actions/setup-chrome`.
- `fatal()` is the terminal error path (`os.Exit(2)`); staticcheck doesn't know
  it's no-return, so guard-then-dereference a pointer inside the `!= nil` branch
  rather than after the guard (see `cmdAttr`).
- Two CI workflows: `test.yml` (cross-OS build + `--version` smoke, upstream's)
  and `check.yml` (the full gate, mirrors `just check-ci`).
