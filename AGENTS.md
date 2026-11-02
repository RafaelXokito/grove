# Agent instructions for grove

Read `SPEC.md` first; it is the source of truth. Add or amend a SPEC section
before implementing a feature.

## Compatibility rule (mandatory)

Every release must be backwards compatible with every earlier release
(SPEC §7):

- Never remove or rename a CLI flag, subcommand, environment variable,
  config key or key binding, and never change its meaning. Add new ones
  instead; deprecated items keep working and warn.
- A config file valid for any earlier release must load with the same
  meaning. Config and cache (`update.json`, `schema` field) formats are
  additive only.
- Release asset names (`grove_<version>_<os>_<arch>.tar.gz`,
  `checksums.txt`) are relied on by `grove update` in already-installed
  binaries: never change them.
- `testdata/compat/<tag>/` snapshots are never edited. When you add a flag,
  env var, config key or subcommand, add it to the newest snapshot that is
  not yet released (`TestCompatSnapshotIsComplete` fails until you do).

## Release process

1. Make sure `main` is green (`gofmt -l .`, `go vet ./...`,
   `go test -race ./...`).
2. Add `testdata/compat/vX.Y.Z/` (copy the previous snapshot, then add what
   is new) and commit it to `main`.
3. Tag `vX.Y.Z` on `main` and push the tag. `.github/workflows/release.yml`
   runs GoReleaser (`.goreleaser.yaml`), which builds darwin/linux ×
   amd64/arm64 archives and `checksums.txt` and publishes the GitHub release.

Do not tag, push or publish releases unless the user asks.
