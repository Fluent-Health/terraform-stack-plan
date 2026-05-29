# Contributing

Thanks for your interest in contributing to tfstackplan.

## Reporting bugs

Open an issue describing the problem, the steps to reproduce it, the command you
ran, and what you expected versus what happened. A minimal `plan.json` (with any
sensitive values redacted) that reproduces the issue is very helpful.

## Proposing changes

1. Fork the repository.
2. Create a branch: `git checkout -b my-fix`.
3. Make your changes and commit with a clear message.
4. Open a pull request against `main`.

## Code style

This is a standard Go module. Before opening a PR:

```bash
gofmt -l .        # must print nothing (run `gofmt -w .` to fix)
go vet ./...      # must be clean
go test ./...     # must pass
```

New behaviour should come with tests. The project follows a test-driven style —
write the failing test first, then the implementation.

## No CLA required

You do not need to sign a Contributor License Agreement. By submitting a pull
request, you agree to license your contribution under the repository's existing
[LICENSE](./LICENSE) (Apache License 2.0).
