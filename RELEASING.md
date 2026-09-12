# Releasing godash

godash ships as several coupled artifacts that must move together:

1. the **Dart package** `godash` on pub.dev,
2. the **Go module** `github.com/nosuta/godash/v2` (a Git tag),
3. the **CLI** `github.com/nosuta/godash/v2/cmd/godash` (installed from that tag),
4. the embedded **project template** and `packages/native_internal` plugin
   (both part of the Go module tree).

## Version scheme

- Git tag / Go module: `vMAJOR.MINOR.PATCH` (the module path is `/v2`, so tags
  are `v2.x.y`).
- `pubspec.yaml` `version:` must equal the tag without the leading `v`
  (`v2.2.10` ↔ `2.2.10`).
- Bump both in the **same commit** that carries the fix.

## Procedure

From a clean `main`:

1. **Bump versions**
   - `pubspec.yaml` → `version: X.Y.Z`
   - `CHANGELOG.md` → new `## X.Y.Z` section
   - If the *minimum* supported version changes, bump the embedded template
     floor:
     - `cmd/godash/assets/_template/pubspec.yaml` → `godash: ^X.Y.Z`
     - `cmd/godash/assets/_template/_go/go.mod.tmpl` →
       `github.com/nosuta/godash/v2 vX.Y.Z`

     Otherwise the CLI pins new projects to its own version at scaffold time
     (`cliVersion()`), so the template floor only matters for local
     `go build` / `go run` builds of the CLI.
2. **Verify** (all must pass)
   - `go vet ./... && go test ./...`
   - `flutter analyze lib test benchmark && flutter test`
   - `dart pub publish --dry-run` — expect only the benign "checked-in files are
     ignored by a .gitignore" warning. The compressed archive must be tens of
     KB, not MB: `.pubignore` is a whitelist and, when present, **replaces**
     `.gitignore`, so it must also exclude `build/`, `.dart_tool/`, etc.
3. **Commit and tag** (fix + version bump in one commit)
   ```sh
   git commit -m "..."
   git tag -a vX.Y.Z -m "release"
   ```
4. **Push** the branch and the tag
   ```sh
   git push origin main && git push origin vX.Y.Z
   ```
5. **Publish the Dart package**
   ```sh
   dart pub publish --force
   ```
   (The template-placeholder warnings are benign.)
6. **Verify the module is fetchable**
   ```sh
   go install github.com/nosuta/godash/v2/cmd/godash@vX.Y.Z
   ```
   If `sum.golang.org` returns 404 immediately after the push, the proxy has not
   indexed the tag yet. Retry in a few minutes, or bypass with
   `GOPROXY=direct GOSUMDB=off go install ...@vX.Y.Z`. Once
   `https://proxy.golang.org/github.com/nosuta/godash/v2/@v/vX.Y.Z.info` returns
   200, normal installs work for everyone.

## Rules

- **Never move or re-tag a published version.** `sum.golang.org` records the
  first hash it sees; moving a tag later breaks `go get` for everyone. Cut a
  new patch version instead.
- **`native_internal` and template fixes are Go-module-scoped.** The
  project-local `.godash/native_internal` plugin is materialised from the module
  version in the *project's* `go.mod`, not from the Dart package or the CLI, so a
  fix there only reaches a project once its `go.mod` requires a module version
  that contains it. Existing projects pick it up with `godash upgrade` (which
  runs `go get -u`).
- **User-owned files are not regenerated.** `lib/main.dart`,
  `lib/app_encryption_key/*`, the platform directories (`ios/`, `macos/`,
  `android/`) and their entitlements belong to the project. Template fixes to
  those only apply to newly scaffolded projects; existing ones need a one-time
  manual patch. Godash-owned files (wiring, proto, web assets, `.godash/`)
  always refresh on `godash prepare` / `godash upgrade`.
- Comments and docs are written in English.
