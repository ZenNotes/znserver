# ZenNotes Server

The self-hosted server for [ZenNotes](https://zennotes.org). It serves the
ZenNotes browser app and its API from a single binary, and keeps your notes as
plain Markdown files in a directory you own.

[![Release](https://img.shields.io/github/v/release/ZenNotes/znserver?label=release)](https://github.com/ZenNotes/znserver/releases/latest)
[![Docker Hub](https://img.shields.io/docker/v/adibhanna/zennotes?label=docker&sort=semver)](https://hub.docker.com/r/adibhanna/zennotes)
[![CI](https://github.com/ZenNotes/znserver/actions/workflows/ci.yml/badge.svg)](https://github.com/ZenNotes/znserver/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

- **One binary, one port.** The browser app is embedded in the server. Open
  `http://<host>:7878` and you are in.
- **Your files stay yours.** The vault is a normal directory of Markdown files.
  Edit it with any other tool; the server picks up changes live.
- **Token login.** A single auth token protects the API. Browser logins use a
  session cookie; the desktop app and scripts send a bearer token.
- **Small footprint.** Static Go binary, no runtime dependencies. The Docker
  image is built `FROM scratch` and runs as a non-root user.

Prebuilt binaries cover Linux, macOS, and Windows on amd64 and arm64. The Docker
image is published for `linux/amd64` and `linux/arm64`.

## Contents

- [Quick start with Docker](#quick-start-with-docker)
- [Install without Docker](#install-without-docker)
  - [Prebuilt binary](#prebuilt-binary)
  - [Run as a systemd service](#run-as-a-systemd-service)
  - [Build from source](#build-from-source)
  - [Nix](#nix)
- [First run](#first-run)
- [Configuration](#configuration)
- [Exposing the server on a network](#exposing-the-server-on-a-network)
- [Upgrading and rolling back](#upgrading-and-rolling-back)
- [Development](#development)
- [Releases](#releases)

## Quick start with Docker

The image is [`adibhanna/zennotes`](https://hub.docker.com/r/adibhanna/zennotes).
It has no shell and runs as UID 65532. It listens on port 7878, serves the
vault from `/workspace`, and keeps server state in `/data`.

### 1. Prepare directories and a token

```sh
mkdir -p "$HOME/ZenNotes/vault" "$HOME/ZenNotes/data"
openssl rand -hex 32 > "$HOME/ZenNotes/auth-token"
chmod 600 "$HOME/ZenNotes/auth-token"
```

Point the vault directory at an existing folder of Markdown files if you already
have one.

### 2. Run the container

```sh
docker run -d --name zennotes \
  --restart unless-stopped \
  --user "$(id -u):$(id -g)" \
  -p 127.0.0.1:7878:7878 \
  -v "$HOME/ZenNotes/vault:/workspace" \
  -v "$HOME/ZenNotes/data:/data" \
  -v "$HOME/ZenNotes/auth-token:/run/secrets/zennotes_auth_token:ro" \
  -e ZENNOTES_AUTH_TOKEN_FILE=/run/secrets/zennotes_auth_token \
  -e ZENNOTES_PERSIST_SESSIONS=1 \
  adibhanna/zennotes:2.51
```

Then open <http://localhost:7878> and paste the token from
`$HOME/ZenNotes/auth-token` when asked.

Why these flags:

- `--user "$(id -u):$(id -g)"` makes the server run as you, so the notes it
  writes are owned by your account. Without it the container runs as UID 65532,
  and both mounted directories must be writable by that UID (`chown 65532`).
- `-p 127.0.0.1:7878:7878` keeps the port off your LAN until you put a TLS
  proxy in front. See [Exposing the server on a network](#exposing-the-server-on-a-network).
- `ZENNOTES_AUTH_TOKEN_FILE` keeps the secret out of `docker inspect` and shell
  history. You can pass `-e ZENNOTES_AUTH_TOKEN=...` directly instead.
- `ZENNOTES_PERSIST_SESSIONS=1` saves browser logins to `/data/sessions.json` so
  a restart does not log everyone out. Leave it off if you prefer sessions never
  touch disk.

### Docker Compose

```yaml
services:
  zennotes:
    image: adibhanna/zennotes:2.51
    restart: unless-stopped
    user: "1000:1000"            # your uid:gid, see `id -u` / `id -g`
    ports:
      - "127.0.0.1:7878:7878"
    volumes:
      - ./vault:/workspace
      - ./data:/data
    environment:
      ZENNOTES_AUTH_TOKEN_FILE: /run/secrets/zennotes_auth_token
      ZENNOTES_PERSIST_SESSIONS: "1"
    secrets:
      - zennotes_auth_token

secrets:
  zennotes_auth_token:
    file: ./auth-token
```

```sh
mkdir -p vault data
openssl rand -hex 32 > auth-token
docker compose up -d
docker compose logs -f
```

The container has no shell or `curl`, so a Compose `healthcheck` cannot run
inside it. Probe `GET /healthz` from the host instead. It returns
`{"ok":true}` without authentication.

### Image tags

| Tag | Meaning |
| --- | --- |
| `2.51.0` | Exact release. Pin this in production. |
| `2.51` | Latest patch of that minor. |
| `latest` | Latest release. |

### Build the image yourself

```sh
docker build -t zennotes-server .
```

The build downloads the pinned browser bundle named in
`web-artifact/manifest.json`, verifies every checksum, and compiles the server.
No Node.js is involved.

## Install without Docker

### Prebuilt binary

Every [release](https://github.com/ZenNotes/znserver/releases) ships static
binaries plus a `SHA256SUMS` file:

| File | Platform |
| --- | --- |
| `zennotes-server-linux-amd64` | Linux x86_64 |
| `zennotes-server-linux-arm64` | Linux ARM64 (Raspberry Pi 4/5, Graviton, Ampere) |
| `zennotes-server-darwin-arm64` | macOS on Apple silicon |
| `zennotes-server-darwin-amd64` | macOS on Intel |
| `zennotes-server-windows-amd64.exe` | Windows x86_64 |

**Linux**

```sh
VERSION=2.51.0
ARCH=amd64   # or arm64
BASE="https://github.com/ZenNotes/znserver/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/zennotes-server-linux-${ARCH}"
curl -fsSLO "${BASE}/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS

chmod +x "zennotes-server-linux-${ARCH}"
sudo install -m 0755 "zennotes-server-linux-${ARCH}" /usr/local/bin/zennotes-server
```

**macOS**

```sh
VERSION=2.51.0
ARCH=arm64   # or amd64 on Intel Macs
BASE="https://github.com/ZenNotes/znserver/releases/download/v${VERSION}"

curl -fsSLO "${BASE}/zennotes-server-darwin-${ARCH}"
curl -fsSLO "${BASE}/SHA256SUMS"
shasum -a 256 --check --ignore-missing SHA256SUMS

chmod +x "zennotes-server-darwin-${ARCH}"
sudo install -m 0755 "zennotes-server-darwin-${ARCH}" /usr/local/bin/zennotes-server
```

The binaries are not notarized. If you download one through a browser instead
of `curl`, macOS quarantines it and Gatekeeper blocks the first run. Clear the
flag with `xattr -d com.apple.quarantine <file>`.

**Windows** (PowerShell)

```powershell
$Version = "2.51.0"
$Base = "https://github.com/ZenNotes/znserver/releases/download/v$Version"
Invoke-WebRequest "$Base/zennotes-server-windows-amd64.exe" -OutFile zennotes-server.exe
Invoke-WebRequest "$Base/SHA256SUMS" -OutFile SHA256SUMS
(Get-FileHash zennotes-server.exe).Hash.ToLower()   # compare with the SHA256SUMS line
```

**Run it**

```sh
ZENNOTES_VAULT_PATH="$HOME/Notes" zennotes-server
```

By default the server binds to `127.0.0.1:7878`, serves `~/ZenNotesVault` if no
vault path is given, and stores its config in `~/.zennotes/server.json`. On a
loopback bind no token is required, and anything on the machine can read and
write the vault. Set a token anyway if other users share the host:

```sh
export ZENNOTES_AUTH_TOKEN="$(openssl rand -hex 32)"
echo "$ZENNOTES_AUTH_TOKEN"        # you will paste this into the browser
ZENNOTES_VAULT_PATH="$HOME/Notes" zennotes-server
```

### Run as a systemd service

```sh
sudo useradd --system --home /var/lib/zennotes --create-home --shell /usr/sbin/nologin zennotes
sudo mkdir -p /var/lib/zennotes/vault /etc/zennotes
sudo chown -R zennotes:zennotes /var/lib/zennotes
printf 'ZENNOTES_AUTH_TOKEN=%s\n' "$(openssl rand -hex 32)" | sudo tee /etc/zennotes/env >/dev/null
sudo chmod 600 /etc/zennotes/env
```

`/etc/systemd/system/zennotes.service`:

```ini
[Unit]
Description=ZenNotes server
After=network-online.target
Wants=network-online.target

[Service]
User=zennotes
Group=zennotes
EnvironmentFile=/etc/zennotes/env
Environment=ZENNOTES_VAULT_PATH=/var/lib/zennotes/vault
Environment=ZENNOTES_CONFIG_PATH=/var/lib/zennotes/server.json
Environment=ZENNOTES_BIND=127.0.0.1:7878
Environment=ZENNOTES_PERSIST_SESSIONS=1
ExecStart=/usr/local/bin/zennotes-server
Restart=on-failure
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/zennotes
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now zennotes
sudo journalctl -u zennotes -f
```

Point `ZENNOTES_VAULT_PATH` at an existing notes folder if you have one, and add
it to `ReadWritePaths`. The service user must be able to write to it.

### Build from source

You need Go 1.25 or newer and `git`. Node.js is not required. The browser app
is pulled in as a pinned, checksummed archive.

```sh
git clone https://github.com/ZenNotes/znserver.git
cd znserver

# Download and verify the pinned browser bundle into web/dist
go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist

# Build the server with the bundle embedded
go build -tags=embed_web -trimpath -ldflags="-s -w" -o bin/zennotes-server ./cmd/zennotes-server

./bin/zennotes-server
```

To cross-compile, set `GOOS` and `GOARCH` on the `go build` line. The result is
a single static binary.

If you only ever connect from the desktop app or from scripts, you can skip the
browser bundle. This builds an API-only server with no web UI from the tip of
`main`:

```sh
go install github.com/ZenNotes/znserver/cmd/zennotes-server@latest
```

Release tags cannot be named here because the module path has no `/v2`
suffix, so `@latest` always means the latest commit. For a specific release,
check out its tag and use the clone-and-build steps above.

### Nix

```sh
nix-build
./result/bin/zennotes-server
```

The derivation uses the version and Go vendor hash pinned in `release.json`
and fetches the browser archive named in `web-artifact/manifest.json`. It never
compiles frontend source.

## First run

1. Open the server URL in a browser. Locally that is <http://localhost:7878>.
2. Paste the auth token when prompted. The browser keeps a session cookie
   afterwards.
3. If no vault is selected yet, click **Connect to server vault** and pick the
   directory. In Docker that is `/workspace`.
4. Write a note, then check the vault directory on disk. The file is there.

**Desktop app.** The ZenNotes desktop app can use a server as its workspace
instead of a local folder. See
[Connect desktop to a remote server](https://github.com/ZenNotes/zennotes/blob/main/docs/how-to/connect-desktop-to-remote-server.md).

**Scripts and other clients.** Send the token as a bearer header:

```sh
curl -H "Authorization: Bearer $ZENNOTES_AUTH_TOKEN" http://localhost:7878/api/notes
```

## Configuration

Settings come from environment variables. A few also live in the host config
file (`server.json`), which the server writes when you select a vault or rotate
the token from the UI. Environment variables win over the file.

### Core

| Variable | Default | Purpose |
| --- | --- | --- |
| `ZENNOTES_VAULT_PATH` | `~/ZenNotesVault` (binary), `/workspace` (Docker) | Directory of Markdown notes to serve. |
| `ZENNOTES_BIND` | `127.0.0.1:7878` (binary), `0.0.0.0:7878` (Docker) | Listen address. |
| `ZENNOTES_CONFIG_PATH` | `~/.zennotes/server.json` (binary), `/data/server.json` (Docker) | Host config file. Persisted sessions are stored next to it. |
| `ZENNOTES_AUTH_TOKEN` | none | Login token. Required on any bind other than loopback. |
| `ZENNOTES_AUTH_TOKEN_FILE` | none | Read the token from a file instead. Used only when `ZENNOTES_AUTH_TOKEN` is unset. Contents are trimmed. |
| `ZENNOTES_BASE_PATH` | none | Serve everything under a path prefix, such as `/notes`, for proxies that route by path. |

### Security

| Variable | Default | Purpose |
| --- | --- | --- |
| `ZENNOTES_BEHIND_TLS` | off | Declare that a TLS-terminating proxy is in front. Marks cookies `Secure` and sends HSTS. |
| `ZENNOTES_TRUSTED_PROXIES` | none | Comma-separated IPs or CIDRs whose `X-Forwarded-*` headers are honoured. Set this when the proxy is not on loopback. |
| `ZENNOTES_ALLOWED_ORIGINS` | none | Comma-separated browser origins allowed to call the API cross-origin. `*` allows all but withholds the session cookie. The desktop app does not need this. |
| `ZENNOTES_BROWSE_ROOTS` | the vault path | Directories the vault picker may offer. Anything outside is rejected. |
| `ZENNOTES_ALLOW_UNSCOPED_BROWSE` | off | Let the vault picker browse the whole filesystem. |
| `ZENNOTES_PERSIST_SESSIONS` | off | Save browser sessions to `sessions.json` (mode `0600`) so logins survive restarts. |
| `ZENNOTES_ALLOW_INSECURE_NOAUTH` | off | Start on a non-loopback bind with no token. Anyone who can reach the port can read and write the vault. |

### Limits and file modes

| Variable | Default | Purpose |
| --- | --- | --- |
| `ZENNOTES_MAX_NOTE_BYTES` | `10485760` (10 MiB) | Largest note, template, or workflow write. |
| `ZENNOTES_MAX_ASSET_BYTES` | `52428800` (50 MiB) | Largest asset upload. |
| `ZENNOTES_VAULT_FILE_MODE` | `0600` | Mode for files the server creates. |
| `ZENNOTES_VAULT_DIR_MODE` | `0700` | Mode for directories the server creates. |

### Advanced

| Variable | Default | Purpose |
| --- | --- | --- |
| `ZENNOTES_DEFAULT_VAULT_PATH` | none | Vault used when neither the env var nor the config file names one. The Docker image sets it to `/workspace`. |
| `ZENNOTES_DISABLE_WATCHER` | off | Turn off the inotify watcher. Live refresh stops but the vault is fully served. Use on unprivileged LXC hosts where inotify wedges. |
| `ZENNOTES_DEV` | off | Accept loopback browser origins, such as a Vite dev server, on a non-loopback bind. |

The startup log prints the effective vault, bind, auth, TLS, and CORS settings,
so you can confirm what the server actually loaded.

## Exposing the server on a network

The server speaks plain HTTP. It refuses to start on a non-loopback bind
without a token, and it warns every 15 minutes while it serves plain HTTP off
loopback. Put a TLS-terminating reverse proxy in front before exposing it.

With [Caddy](https://caddyserver.com) on the same host:

```
notes.example.com {
    reverse_proxy 127.0.0.1:7878
}
```

Then tell the server it is behind TLS:

```sh
ZENNOTES_BEHIND_TLS=1
```

If the proxy talks to the server from another address, for example over a
Docker bridge network, also set `ZENNOTES_TRUSTED_PROXIES` to that network so
forwarded headers and client rate limits work:

```sh
ZENNOTES_TRUSTED_PROXIES=172.16.0.0/12
```

The live-update feed at `/watch` is a WebSocket. Caddy proxies it as-is. For
nginx, forward the `Upgrade` and `Connection` headers on that path.

For the full checklist, see
[Secure self-hosting](https://github.com/ZenNotes/zennotes/blob/main/docs/how-to/secure-self-hosting.md)
in the main repository.

## Upgrading and rolling back

Notes are plain files and there are no vault migrations. Upgrading and rolling
back both mean running a different server version against the same directory.

- **Docker:** change the image tag, then `docker compose pull && docker compose up -d`.
- **Binary:** replace the file in `/usr/local/bin` and restart the service.
- **Roll back:** run the previous tag or binary. Nothing in the vault needs to
  be undone.

The server logs its version first on startup, as `ZenNotes Server: vX.Y.Z`,
so you can confirm which release is running after an upgrade or rollback.

Each server release pins one browser bundle, so upgrading the server upgrades
the web app with it.

## Development

Go 1.25 or newer is enough for API work. No frontend toolchain is needed.

```sh
go vet ./...
go test ./...
go run ./cmd/zennotes-server
```

Without the `embed_web` tag the server runs API-only and logs a warning about
the missing bundle. That is the expected mode for backend development, with
the browser app served separately from the
[main ZenNotes repository](https://github.com/ZenNotes/zennotes).

To test the embedded bundle exactly as it ships:

```sh
go run ./cmd/prepare-web -manifest web-artifact/manifest.json -output web/dist
go test -tags=embed_web ./web
go build -tags=embed_web -trimpath -o bin/zennotes-server ./cmd/zennotes-server
```

### How the browser bundle is pinned

The main ZenNotes repository builds the browser app and publishes an immutable
`.tgz` together with a manifest that records the protocol, source commit,
toolchain, archive checksum, and a checksum for every file. This repository
commits only the manifest, under `web-artifact/manifest.json`.

`prepare-web` reads that manifest and installs the archive from one of three
places: an explicit `-archive` path, a `.tgz` beside the manifest, or the HTTPS
URL in the manifest. It rejects unknown protocols, path traversal, links,
unexpected or duplicate files, size violations, and checksum mismatches before
exposing any file. The reviewed manifest is the trust anchor. Checksums catch a
changed download, not a replaced manifest, so manifest updates go through code
review with the server source.

Uncommitted browser candidates need `-allow-dirty` and have no release URL.
Release and CI builds never pass that flag. The output directory is guarded by
an `.install-lock` file, so use a private build directory and, if an installer
crashes, confirm it has exited before removing the stale lock.

CI runs the Go tests on Linux, macOS, and Windows, builds the Docker image for
both architectures without pushing, and runs `nix-build` with the pinned
toolchain.

## Releases

Server versions live in `release.json` and are tagged `vX.Y.Z`. A release is a
server commit plus the browser manifest it pins. To ship one:

1. Update `web-artifact/manifest.json` if the browser app changed, and review
   the diff.
2. Bump `version` in `release.json` and, if Go dependencies changed, the
   `vendorHash`.
3. Run the **Prepare server release** workflow with the reviewed commit SHA and
   the new tag. It cross-compiles the binaries, writes `SHA256SUMS`, and opens a
   draft GitHub release.
4. Verify a candidate install and a rollback, then publish the draft.
5. Run the **Publish Docker image** workflow from the new release tag (not from
   `main`) to push the multi-arch image to Docker Hub with the version, minor,
   and `latest` tags. Its environment gate asks a reviewer to approve the push.
   Run from a branch, only the optional extra tag is published.

## License

[MIT](LICENSE)
