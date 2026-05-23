# Contributing

Thanks for your interest. This is a small toolkit, kept deliberately simple.

## Building and testing

```
go build ./...
go test ./...
go vet ./...
```

`cmd/cass` uses SQLite via `modernc.org/sqlite` by default (pure Go, no CGO).
The DuckDB backend is experimental and only builds when explicitly requested
with the `duckdb` build tag.

## Code style

The project follows the [Russ Cox style](https://research.swtch.com/) the
maintainer uses across his Go work:

- Small, focused interfaces.
- `io.Reader` / `io.Writer` over custom streaming interfaces.
- Errors over panics; `fmt.Errorf("action: %w", err)` for wrapping.
- One concern per package; minimal dependencies.
- Doc comments on every exported symbol; package docs in `doc.go`.
- Examples preferred over prose. Use `Example` and `ExampleType_Method` tests.
- Table-driven tests with `[]struct{name, input, want}`.

When in doubt, look at the existing code for the style.

## Pull requests

- Keep commits atomic. One coherent change per commit.
- Run `go build ./...` and `go test ./...` before pushing.
- Update or add doc comments alongside code changes.
- For new commands, add a `doc.go` (see `cmd/ccfmt/doc.go` as a template).

## Reporting issues

Open a GitHub issue with:

- The command you ran.
- The output you got, including stderr.
- The output you expected.
- Your Go version (`go version`) and OS.

A reproducible session JSONL or minimal repro repository helps a lot.
