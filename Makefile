.PHONY: test vet examples release release-contrib dev-restore

# Run all core tests with race detection.
test:
	go test -race -count=1 ./...

# Run vet on core and all contrib modules.
vet:
	go vet ./...
	cd llm && go vet ./...
	cd otel && go vet ./...
	cd river/outbox && go vet ./...

# Run every example (intake_service is build-only since it starts a server).
examples:
	go run ./examples/malformed_json/
	go run ./examples/review_loop/
	go run ./examples/pipeline_ring/
	go run ./examples/stream_router/
	go run ./examples/fanout_consensus/
	go run ./examples/support_agent/
	go build ./examples/intake_service/

# ---------------------------------------------------------------------------
# Release
#
# Usage:
#   make release VERSION=0.1.0
#
# This tags and pushes the core module. After the module proxy indexes it
# (usually a few minutes), run:
#
#   make release-contrib VERSION=0.1.0
#
# to update contrib modules and tag them.
# ---------------------------------------------------------------------------

# Phase 1: Tag and push the core module.
release:
	@test -n "$(VERSION)" || (echo "VERSION is required — usage: make release VERSION=0.1.0" && exit 1)
	@echo "==> Preparing core v$(VERSION)"
	cd llm && go mod edit -dropreplace=github.com/vertexbuild/reflow
	cd otel && go mod edit -dropreplace=github.com/vertexbuild/reflow
	cd river/outbox && go mod edit -dropreplace=github.com/vertexbuild/reflow
	git add llm/go.mod otel/go.mod river/outbox/go.mod
	git commit -m "Remove replace directives for v$(VERSION) release"
	git tag v$(VERSION)
	git push origin main v$(VERSION)
	@echo ""
	@echo "==> Core v$(VERSION) tagged and pushed."
	@echo "    Wait for the module proxy to index it, then run:"
	@echo ""
	@echo "    make release-contrib VERSION=$(VERSION)"

# Phase 2: Update contrib modules to reference the tagged core version.
release-contrib:
	@test -n "$(VERSION)" || (echo "VERSION is required — usage: make release-contrib VERSION=0.1.0" && exit 1)
	@echo "==> Updating contrib modules to v$(VERSION)"
	cd llm && go mod edit -require=github.com/vertexbuild/reflow@v$(VERSION) && go mod tidy
	cd otel && go mod edit -require=github.com/vertexbuild/reflow@v$(VERSION) && go mod tidy
	cd river/outbox && go mod edit -require=github.com/vertexbuild/reflow@v$(VERSION) && go mod tidy
	git add llm/ otel/ river/
	git commit -m "Update contrib modules to core v$(VERSION)"
	git tag llm/v$(VERSION)
	git tag otel/v$(VERSION)
	git tag river/outbox/v$(VERSION)
	git push origin main llm/v$(VERSION) otel/v$(VERSION) river/outbox/v$(VERSION)
	@echo ""
	@echo "==> Contrib modules tagged and pushed."
	@echo "    Run 'make dev-restore' to restore replace directives for development."

# Restore replace directives for continued local development.
dev-restore:
	cd llm && go mod edit -replace=github.com/vertexbuild/reflow=..
	cd otel && go mod edit -replace=github.com/vertexbuild/reflow=..
	cd river/outbox && go mod edit -replace=github.com/vertexbuild/reflow=../..
	git add llm/go.mod otel/go.mod river/outbox/go.mod
	git commit -m "Restore replace directives for development"
	git push origin main
