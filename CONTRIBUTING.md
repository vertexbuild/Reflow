# Contributing

## Local setup

The repo uses [Go workspaces](https://go.dev/doc/tutorial/workspaces) so the core module and contrib modules resolve to your local tree during development.

```
git clone https://github.com/vertexbuild/reflow.git
cd reflow
go work init . ./contrib/llm ./contrib/otel ./contrib/river/outbox
```

This creates a `go.work` file (gitignored) that tells Go to use all local modules together. Without it, contrib modules would try to fetch the published core version instead of your working copy.

## Running tests

```
make test       # core tests with -race
make vet        # vet core + all contrib
make examples   # run every example
```

## Releasing

Releases are two-phase because the Go module proxy needs to index the core module before contrib modules can reference it.

```
# Phase 1: tag and push core
make release VERSION=0.1.0

# Wait a few minutes for proxy.golang.org to index it, then:

# Phase 2: update and tag contrib modules
make release-contrib VERSION=0.1.0

# Restore replace directives for development
make dev-restore
```

Tags follow the Go multi-module convention:
- Core: `v0.1.0`
- Contrib: `llm/v0.1.0`, `otel/v0.1.0`, `river/outbox/v0.1.0`
