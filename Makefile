# rodney: rodrigolive fork of simonw/rodney. Run `make` for help.

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
INSTALL_DIR ?= $(HOME)/util
UPSTREAM    ?= upstream
ARGS        ?= --help

.DEFAULT_GOAL := help
.PHONY: help build vet test test-sh run install upstream clean

help: ## Show this help
	@echo "rodney $(VERSION): Chrome automation CLI (rodrigolive fork of simonw/rodney)"
	@echo
	@echo "Usage: make <target> [VAR=value]"
	@echo
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*## "} /^[a-z][a-z-]*:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo
	@echo "Variables:"
	@echo "  VERSION      stamped into the binary (default: git describe, now $(VERSION))"
	@echo "  INSTALL_DIR  where 'make install' puts the binary (default: $(INSTALL_DIR))"
	@echo "  UPSTREAM     git remote for simonw/rodney (default: $(UPSTREAM))"
	@echo "  ARGS         arguments for 'make run' (default: $(ARGS))"
	@echo
	@echo "Examples:"
	@echo "  make build && ./rodney version"
	@echo "  make run ARGS='open https://example.com'"
	@echo "  make install              # vet + build, then copy to $(INSTALL_DIR)/rodney"
	@echo "  make upstream             # list simonw/rodney commits not merged here yet"
	@echo
	@echo "Tests boot a real headless Chrome; rod downloads Chromium unless ROD_CHROME_BIN is set."

build: ## Build ./rodney with the git version stamped in
	go build -trimpath -ldflags="$(LDFLAGS)" -o rodney .

vet: ## go vet, gofmt check, and compile the tests without running them
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go test -c -o /dev/null .

test: ## Full Go test suite (boots one headless Chrome)
	go test ./... -count=1

test-sh: build ## Bash integration harness (./test.sh) in a throwaway RODNEY_HOME
	go build -o /tmp/testserver ./tests/testserver
	RODNEY_HOME=$$(mktemp -d) ./test.sh

run: ## Run from source: make run ARGS='open https://example.com'
	go run . $(ARGS)

install: vet build ## Copy the built binary to INSTALL_DIR
	install -m 0755 rodney $(INSTALL_DIR)/rodney
	@echo "installed rodney $(VERSION) -> $(INSTALL_DIR)/rodney"

upstream: ## Fetch simonw/rodney and list commits not merged here
	git fetch $(UPSTREAM)
	@git log --oneline HEAD..$(UPSTREAM)/main || true

clean: ## Remove build outputs
	rm -f rodney rodney-*
