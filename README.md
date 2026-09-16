# ZenNotes self-hosted server

This Go service owns filesystem access, the self-hosted HTTP API, authentication,
configuration, and vault watching. Laravel owns ZenNotes Cloud accounts, billing,
sync, and publishing in the separate private website repository.

The planned server repository is [ZenNotes/znserver](https://github.com/ZenNotes/znserver).
Source and distribution channels still live in the main repository during the migration.
The Go module path stays unchanged until the extraction checkpoint is ready.

## Develop and test with Go alone

Requires Go 1.25 or later. From this directory:

```sh
go vet ./...
go test ./...
go run ./cmd/zennotes-server
```

These commands do not require Node, npm, a sibling checkout, or `web/dist`.
The default binary serves the API. It logs that the browser bundle is absent.
For browser development, run the Vite client separately with `npm run dev:web`
from the main repository and use a dedicated test vault and auth token.

## Build a binary with the browser app

Production builds use Go's `embed_web` build tag. They require the web distribution
under `web/dist`, including `index.html`. Missing assets fail compilation or the
bundle check. Go's [build constraints](https://pkg.go.dev/cmd/go#hdr-Build_constraints)
keep the [embedded assets](https://pkg.go.dev/embed) out of ordinary Go tests.

From the main repository:

```sh
npm run build --workspace @zennotes/web
npm run build --workspace @zennotes/server
```

The server build stages the web distribution and holds the existing asset lock
through the bundle check and Go compilation. Its output is `bin/zennotes-server`
(`bin/zennotes-server.exe` on Windows).

With a prebuilt web distribution already staged, the standalone Go commands are:

```sh
go test -tags=embed_web ./web
go build -tags=embed_web -trimpath -o bin/zennotes-server ./cmd/zennotes-server
```

Docker and the server Nix package also select `embed_web`. Existing binary names,
configuration variables, API routes, authentication, and vault formats are unchanged.
The source-based distribution channels stay in place during the migration.

## Build from a pinned browser archive

The main repository can produce a candidate with `npm run artifact:web`. Its output
under `dist/web-artifacts` contains an immutable `.tgz` and a JSON manifest naming
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
release URL. No artifacts from this migration have been published. The candidate
CI workflow only transfers build artifacts between jobs; it does not make a release.

Use a privately owned build directory. Concurrent cooperating installs are rejected
using an exclusive `.install-lock` beside the output. Other processes must not
modify the output or its parents during the build. If an installer crashes, confirm
it has exited before removing its stale lock. Existing matching assets are verified
and reused; a different build requires a new clean output directory.

The importer rejects unsupported protocols, ambiguous manifests, traversal, links,
unexpected or duplicate files, size violations, and checksum mismatches before
exposing assets. The protocol marker `self-hosted-http-v1` identifies the tested
HTTP behavior; it is independent of the legacy `/api/version` response.
