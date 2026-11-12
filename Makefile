.PHONY: check fmt vet test race py hooks

# PKGS is empty until the first Go package exists; vet/test then skip instead of failing.
PKGS := $(shell go list ./... 2>/dev/null)

# check is the gate before every commit (DESIGN.md §11).
check: fmt vet test race py

# fmt fails if any file is not gofmt-formatted. It lists files, never rewrites them.
fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(if $(PKGS),go vet ./...,@echo "vet: no Go packages yet")

test:
	$(if $(PKGS),go test ./...,@echo "test: no Go packages yet")

# race runs every test with the race detector. The codebase is small, so we run
# all packages instead of only limiter/queue/retry.
race:
	$(if $(PKGS),go test -race ./...,@echo "race: no Go packages yet")

# py runs the Python script tests (stdlib unittest, no extra packages).
py:
	python3 -m unittest discover -s scripts -q

# hooks points git at the versioned hook directory so `make check` runs on commit.
hooks:
	git config core.hooksPath .githooks
