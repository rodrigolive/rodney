version := `git describe --tags --always --dirty 2>/dev/null || echo dev`
ldflags := "-s -w -X main.version=" + version

# Build the rodney binary with the version stamped from git.
build:
    go build -trimpath -ldflags="{{ldflags}}" -o rodney .

tidy:
    go mod tidy

# Install the Go dev tools pinned in go.mod's `tool` block
# (golangci-lint, goimports, gofumpt, dlv) into $GOBIN. Run once after cloning.
bootstrap:
    go install tool

fmt:
    gofmt -w .
    go tool goimports -w .

lint:
    go tool golangci-lint run ./...

vet:
    go vet ./...

# Full suite. TestMain boots one headless Chrome via rod (~8s); rod
# auto-downloads Chromium if none is found, or set ROD_CHROME_BIN.
test:
    go test ./... -count=1

# Race-detector pass (~2x slower). CI-only / before merging concurrency changes.
test-race:
    go test ./... -race -count=1

# bash integration harness (needs a built ./rodney + a usable Chrome).
test-sh: build
    ./test.sh

# Run rodney from source: `just run open https://example.com`
run *ARGS:
    go run . {{ARGS}}

# Local pre-commit gate — run after every change.
check: tidy fmt vet lint test build

ok: check
    @echo "All checks passed."

# CI gate — verify-only (no file writes). Mirrors `check` so local and CI
# can't drift: add a step here and .github/workflows/check.yml picks it up.
check-ci:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "==> tidy (verify go.mod / go.sum committed)"
    go mod tidy
    git diff --exit-code go.mod go.sum
    echo "==> fmt (verify gofmt + goimports clean)"
    out=$(gofmt -l .); if [ -n "$out" ]; then echo "gofmt drift:"; echo "$out"; exit 1; fi
    out=$(go tool goimports -l .); if [ -n "$out" ]; then echo "goimports drift:"; echo "$out"; exit 1; fi
    echo "==> vet"
    go vet ./...
    echo "==> lint"
    go tool golangci-lint run ./...
    echo "==> test"
    go test ./... -count=1
    echo "==> build"
    go build -trimpath -ldflags="{{ldflags}}" -o rodney .

clean:
    rm -f rodney
