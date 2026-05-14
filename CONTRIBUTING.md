# Contributing

scorpius is in early development. Design decisions and scope boundaries are
recorded under `docs/adr/`. Read the ADRs and the README before opening a
pull request.

## Development setup

Requirements:

- Go 1.23 or later
- `staticcheck` (`go install honnef.co/go/tools/cmd/staticcheck@latest`)
- Linux with `iproute2` (`tc`) available for integration tests
- Root, or a user namespace with `CAP_NET_ADMIN`, for integration tests

## Common tasks

```
make build      # static binary into ./bin
make test       # unit tests
make lint       # go vet plus staticcheck
make fmt        # gofmt over the tree
make clean      # remove ./bin
```

Integration tests live behind a build tag and require root. Run them with:

```
go test -tags=integration ./...
```

## Coding standards

- `gofmt`, `go vet`, and `staticcheck` all clean.
- Errors wrap with `fmt.Errorf("context: %w", err)`.
- Structured logging via `log/slog`.
- Standard library first. Justify new dependencies.
- No em dashes in user-facing text or documentation.

## Commits

Format: `component: short summary`, with a body explaining why where useful.
Reference the relevant ADR if applicable. Commits are squash-merged on PR.

## Code of conduct

See `CODE_OF_CONDUCT.md`. By participating you agree to abide by it.
