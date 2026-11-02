# Contributing

```sh
gofmt -l . && go vet ./... && go test -race -count=1 ./...
```

`SPEC.md` is the source of truth: change the spec first, then the code.

## Compatibility policy

From v0.1.0 on, every release is backwards compatible with every earlier
one (SPEC §7). Flags, subcommands, environment variables, config keys and key
bindings are never removed, renamed or given a new meaning; deprecations keep
working and warn; config and cache formats only grow.

This is enforced by `testdata/compat/<tag>/`: a `config.toml` using every
config key of that release and an `invocations.json` listing every flag, env
var and subcommand with the settings they must resolve to. The tests in
`cmd/grove/compat_test.go` replay every snapshot against the current code.
**Each release adds its own snapshot directory** (copy the previous one, add
what is new); existing snapshots are never edited.

## Releasing

Tag `vX.Y.Z` on `main` and push the tag; `.github/workflows/release.yml`
runs GoReleaser and publishes `grove_<version>_<os>_<arch>.tar.gz` for
darwin/linux × amd64/arm64 plus `checksums.txt`. Validate config changes
with `go run github.com/goreleaser/goreleaser/v2@v2.12.7 check`.
