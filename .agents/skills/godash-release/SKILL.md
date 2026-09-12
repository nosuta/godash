---
name: godash-release
description: Use when cutting/releasing a godash version — bumping pubspec.yaml + CHANGELOG, tagging vX.Y.Z, pushing the Go module tag, publishing to pub.dev, or when `sum.golang.org`/`go install ...@vX` fails after a release. Triggers on "release godash", "publish godash", "cut a release", "bump version", "v2.x.y".
---

# Releasing godash

Full checklist: `RELEASING.md`. The artifacts move together: the Dart package
(pub.dev), the Go module tag, the CLI, and the embedded template +
`packages/native_internal`.

## Steps

1. Bump `pubspec.yaml` `version:` and add a `CHANGELOG.md` entry in the **same**
   commit as the fix. Bump the template floor only when the minimum changes
   (`cmd/godash/assets/_template/pubspec.yaml` and `_go/go.mod.tmpl`).
2. Verify: `go vet ./... && go test ./...`, `flutter analyze lib test benchmark`,
   `flutter test`, `dart pub publish --dry-run` (archive must be tens of KB —
   `.pubignore` replaces `.gitignore`).
3. `git commit` then `git tag -a vX.Y.Z -m "release"`.
4. `git push origin main && git push origin vX.Y.Z`.
5. `dart pub publish --force`.
6. `go install github.com/nosuta/godash/v2/cmd/godash@vX.Y.Z`.

## Rules

- **Never move/re-tag a published version** — `sum.golang.org` pins the first
  hash; retagging breaks `go get` for everyone. Cut a new patch instead.
- If `sum.golang.org/...@vX.Y.Z` 404s right after pushing, the proxy has not
  indexed it yet: retry in a few minutes or use
  `GOPROXY=direct GOSUMDB=off go install ...@vX.Y.Z`.
- `native_internal`/template fixes only reach a project whose `go.mod` requires
  a module version containing them (`godash upgrade` runs `go get -u`).
- User-owned files (`lib/main.dart`, `lib/app_encryption_key/*`, platform dirs
  and entitlements) are never regenerated; only new `godash new` projects get
  template fixes there.
