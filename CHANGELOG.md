## 2.2.0

* Publish godash to pub.dev so projects consume it as `godash: ^2.2.0` instead
  of a local `path: ../godash` checkout.
* Resolve the godash code-generation tools and `godash/options.proto` from the
  project's own Go module graph (`$GODASH_MODULE_DIR`); the Go module is now
  consumed from the module cache with no `replace` directive.
* Materialise the `native_internal` Flutter plugin into the project-local
  `.godash/native_internal` directory on prepare/build.
* `godash upgrade` for version-pinned projects runs `flutter pub upgrade
  godash native_internal` and `go get -u github.com/nosuta/godash/v2`.
* Keep auto-provisioning of a godash checkout for path-replace projects.
