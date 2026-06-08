# Eruditto top-level Makefile
#
# Conventions:
#   - Recipes use tabs (not spaces) for indentation.
#   - .PHONY is used for every recipe so behavior is stable regardless of
#     stray files in the working tree.

.PHONY: help smoke build test vet fmt clean

help:
	@echo "Eruditto - available targets:"
	@echo "  make smoke   - run the Phase 0 environment smoke test (Fyne, CGO, X11)"
	@echo "  make build   - build the eruditto binary into ./build/"
	@echo "  make test    - run go test ./..."
	@echo "  make vet     - run go vet ./..."
	@echo "  make fmt     - run gofmt -w on the whole tree"
	@echo "  make clean   - remove ./build/ and test artifacts"

smoke:
	@bash scripts/smoke-test.sh

build:
	@mkdir -p build
	@go build -o build/eruditto ./cmd/eruditto

test:
	@go test ./...

vet:
	@go vet ./...

fmt:
	@gofmt -w .

clean:
	@rm -rf build
	@rm -f coverage.out coverage.html
