# ZenNotes self-hosted server

The Go server owns the self-hosted API, authentication, filesystem access, and
vault watching. The main ZenNotes repository owns the browser app and publishes
its immutable build. Laravel Cloud remains a separate service.

## Build and verify

Go 1.25 or later is sufficient for API development:

```sh
go vet ./...
go test ./...
go run ./cmd/zennotes-server
```

Production bundles include the browser artifact pinned in
`web-artifact/manifest.json`:

```sh
go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist
go test -tags=embed_web ./web
go build -tags=embed_web -trimpath -o bin/zennotes-server ./cmd/zennotes-server
```

No Node install or sibling source checkout is needed. The importer checks
protocol, source, tar paths/types, size, archive SHA-256, and every file checksum.
The reviewed manifest is the trust anchor. Dirty local candidates require
`-allow-dirty`; release and CI paths deliberately omit that flag.

## Build from a pinned browser archive

The main ZenNotes repository produces the browser artifact (`npm run artifact:web`
there). Its output contains an immutable `.tgz` and a JSON manifest naming
the protocol, source commit, toolchain, archive checksum, and every asset checksum.
This is separate from desktop releases and from the public share viewer.

In a clean server build directory, place the reviewed manifest next to its archive:

```sh
go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist
go test -tags=embed_web ./web
go build -tags=embed_web -trimpath -o bin/zennotes-server ./cmd/zennotes-server
```

Only the artifact producer needs Node. The Go consumer accepts an adjacent archive,
an explicit `-archive` path, or the manifest's HTTPS URL. The manifest is the trust
anchor and must be reviewed and pinned with the server source. Checksums detect
changed downloads; they do not authenticate an independently replaced manifest.

Uncommitted source candidates require `-allow-dirty` for local testing and have no
release URL. 
Use a privately owned build directory. Concurrent cooperating installs are rejected
using an exclusive `.install-lock` beside the output. Other processes must not
modify the output or its parents during the build. If an installer crashes, confirm
it has exited before removing its stale lock. Existing matching assets are verified
and reused; a different build requires a new clean output directory.

The importer rejects unsupported protocols, ambiguous manifests, traversal, links,
unexpected or duplicate files, size violations, and checksum mismatches before
exposing assets. The protocol marker `self-hosted-http-v1` identifies the tested
HTTP behavior; it is independent of the legacy `/api/version` response.

## Distribution

`docker build .` builds the same Go-only distribution. Preserve image ownership
and the existing `adibhanna/zennotes` image when the publisher cutover is approved.
Runtime defaults remain UID 65532, port 7878, `/workspace`, `/data/server.json`, and
the existing `ZENNOTES_*` variables. Existing authentication/base-path behavior
and note bytes are covered by HTTP fixtures under `internal/httpserver/testdata`.

`nix-build` uses the pinned browser archive and Go vendor hash in `release.json`.
It never compiles frontend source. A local rehearsal can use
`nix-build --arg allowDirty true` with an adjacent candidate archive.

To update the web app, review a new manifest, run Go/import/embed/HTTP tests, and
release it with the server source. Roll back by selecting the prior server image
or binary and restoring its pin; no vault migration is introduced.

## Extraction gate

This directory is prepared in a local rehearsal before publication. Do not enable
its Docker publisher while the main repository still publishes the same tags.
First approve and publish a clean browser artifact, extract approved history,
verify destination CI and candidate installation, then switch one channel at a
time. The main repository retains its source and previous release until a verified
destination release and rollback rehearsal exist.
