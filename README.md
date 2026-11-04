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

Prebuilt binaries cover Linux and macOS on amd64 and arm64, and Windows on
amd64. The Docker image is published for `linux/amd64` and `linux/arm64`.

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
- [MCP for AI assistants](#mcp-for-ai-assistants)
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
  adibhanna/zennotes:2.69
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
    image: adibhanna/zennotes:2.69
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

The image reports its health to Docker on its own: `docker ps` shows `healthy`
or `unhealthy`, and Compose's `depends_on: condition: service_healthy` works
with no extra setup. It runs `/zennotes-server healthcheck`, because the
container has no shell or `curl`. To change the timing, set your own in Compose:

```yaml
healthcheck:
  test: ["CMD", "/zennotes-server", "healthcheck"]
  interval: 30s
  timeout: 5s
  retries: 3
  start_period: 30s
```

The check means the server is answering, not that the vault is readable. From
outside the container, `GET /healthz` returns `{"ok":true}` without
authentication.

A proxy that reads Docker health, such as Traefik, routes to the container only
once it is healthy. The first check runs a second after start on Docker 25 or
later, and after 30 seconds on older versions. To opt out, set
`healthcheck: { disable: true }` in Compose.

### Image tags

| Tag | Meaning |
| --- | --- |
| `2.82.1` | Exact release. Pin this in production. |
| `2.78` | Latest patch of that minor. |
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
VERSION=2.82.1
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
VERSION=2.82.1
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
$Version = "2.82.1"
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

You need Go 1.26 or newer and `git`. Node.js is not required. The browser app
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

### MCP

Off unless `ZENNOTES_MCP_ENABLED` is on. See [MCP for AI assistants](#mcp-for-ai-assistants).

| Variable | Default | Purpose |
| --- | --- | --- |
| `ZENNOTES_MCP_ENABLED` | off | Serve MCP at `/mcp`. Needs `ZENNOTES_AUTH_TOKEN` and `ZENNOTES_VAULT_PATH`. While off, the route does not exist and the variables below are not read. |
| `ZENNOTES_MCP_TOKEN` | none | The MCP token for one assistant: 32 characters or more, different from `ZENNOTES_AUTH_TOKEN`. |
| `ZENNOTES_MCP_TOKEN_FILE` | none | Read that token from a file instead. Contents are trimmed. |
| `ZENNOTES_MCP_ACCESS` | none, required | `read`, `comment` or `write`. |
| `ZENNOTES_MCP_ALLOW_PATHS` | whole vault | JSON array of folders and notes to share, such as `["Projects","Areas/Health"]`. Everything else does not exist for the assistant. Blank, `[]` and `null` are refused: leave it out to share the whole vault. |
| `ZENNOTES_MCP_DENY_PATHS` | none | JSON array of folders and notes to hide, even inside an allowed one. |
| `ZENNOTES_MCP_CREDENTIALS_FILE` | none | Several assistants, each with its own token and scope. Cannot be combined with the token, access and path variables. |
| `ZENNOTES_MCP_ALLOWED_ORIGINS` | none | Comma-separated `Origin` values to accept. Any other `Origin` gets a 403. MCP clients outside a browser send none, so you rarely need this. For a web page to call `/mcp`, its origin must also be in `ZENNOTES_ALLOWED_ORIGINS`, which sends the CORS headers. |

The startup log prints the effective vault, bind, auth, TLS, CORS, and MCP settings,
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

## MCP for AI assistants

Since server 2.83.0, the server can serve [MCP](https://modelcontextprotocol.io) itself,
so assistants on other machines can use the vault without `zn` installed anywhere. It is
off by default. The tools and access rules are the same ones `zn mcp` uses, built into the
server, so there is no second program to install or version to match.

### One assistant

```yaml
services:
  zennotes:
    image: adibhanna/zennotes:2.83
    environment:
      - ZENNOTES_AUTH_TOKEN=${ZENNOTES_AUTH_TOKEN:?}
      - ZENNOTES_VAULT_PATH=/workspace
      - ZENNOTES_MCP_ENABLED=1
      - ZENNOTES_MCP_TOKEN=${ZENNOTES_MCP_TOKEN:?}
      - ZENNOTES_MCP_ACCESS=read
      - 'ZENNOTES_MCP_ALLOW_PATHS=["Projects"]'
    volumes:
      - ./vault:/workspace
      - ./data:/data
```

Make the token with `openssl rand -hex 32`. Paths are the ones `zn` and the app show: in a
vault with an inbox folder, that is `["inbox/Projects"]`, and an entry that matches nothing
shares nothing. Then, for a client that can send a
header, such as Claude Code:

```sh
claude mcp add --transport http zennotes https://notes.example.com/mcp \
  --header "Authorization: Bearer $ZENNOTES_MCP_TOKEN"
```

Under `ZENNOTES_BASE_PATH=/notes` the route is `/notes/mcp`. Use HTTPS for anything
off the machine; see [Exposing the server on a network](#exposing-the-server-on-a-network).

### Several assistants, each with its own scope

Point `ZENNOTES_MCP_CREDENTIALS_FILE` at a file like this. It holds each token's
SHA-256, not the token, so the token exists only with the client that uses it.

```json
{
  "version": 1,
  "credentials": [
    { "name": "chatgpt", "tokenSha256": "<64 hex digits>", "access": "read", "allow": ["Projects"] },
    { "name": "reviewer", "tokenSha256": "<64 hex digits>", "access": "comment", "allow": ["Areas"], "deny": ["Areas/Health"] }
  ]
}
```

```sh
printf %s "$TOKEN" | shasum -a 256
```

The token alone decides the scope, and nothing a client sends can widen it. To change
or revoke a credential, edit the file and restart the server. `name` appears in the
log and signs the credential's comments. Use `openssl rand -hex 32` here too: the server only
sees the hash, so it cannot tell you a token is too short.

### Through OpenAI's Secure MCP Tunnel

```yaml
  zennotes-mcp:
    image: ghcr.io/openai/tunnel-client:v0.0.19
    environment:
      - CONTROL_PLANE_API_KEY=${CONTROL_PLANE_API_KEY:?}
      - CONTROL_PLANE_TUNNEL_ID=${CONTROL_PLANE_TUNNEL_ID:?}
      - MCP_SERVER_URL=http://zennotes:7878/mcp
      - "MCP_EXTRA_HEADERS=Authorization: Bearer ${ZENNOTES_MCP_TOKEN:?}"
```

The quotes on the last line matter: without them, the `: ` makes it invalid YAML.
This hop has not been tested yet, because it needs an OpenAI tunnel. Per OpenAI's
documentation, tool calls and their results pass through OpenAI, and the scope limits
how much of the vault can make that trip.

### What to know

- **Tokens are separate.** An MCP token opens `/mcp` and nothing else: the REST API,
  the browser app and login all refuse it. The server's token and browser sessions
  do not open `/mcp`, so a full-access assistant needs an MCP token as well. Rotating
  the server's token leaves MCP working, and it cannot be rotated to an MCP token.
- **The server refuses to start** with MCP on if there is no `ZENNOTES_AUTH_TOKEN` or
  `ZENNOTES_VAULT_PATH`, a token is too short or is the server's own, access is not stated,
  or a path, a list or the credentials file is invalid. A misspelled field in the file is an
  error too, so it can never leave a credential seeing more than you meant.
- **The vault stays put.** With `ZENNOTES_VAULT_PATH` set, the server refuses to switch
  vaults, so a token can never end up on another one.
- **Each request stands alone.** `/mcp` issues no session IDs and answers `GET` with 405.
- **Limits.** At most 32 requests run at once, and 64 MiB of request bodies; past that they
  get a 503. With several credentials, each may use at most half of the 32. A credential that
  can write can send a note up to `ZENNOTES_MAX_NOTE_BYTES` or a 16 MiB attachment. Others can
  send 1 MiB. The note limit applies after JSON encoding, so a note full of quotes or line
  breaks reaches it at about half the size.
- **Concurrent edits.** Assistants editing one note take turns. Someone editing it elsewhere
  at that moment, in the browser or with `zn`, can still be overwritten.
- **Logs.** A tool call's line holds the credential's name, the tool, the outcome and the
  duration, never tokens, arguments or results. Requests with no valid token are counted, at
  most once a minute. Other log lines can name a path, such as a failed operation or a deny
  entry that matches nothing.

**Not in this version:**

- An assistant restricted to certain paths is not offered note renames, or attachment reads,
  renames, moves or copies. Renames are out because the server rewrites links in every note,
  including ones the assistant cannot see. This is the same as `zn mcp` against a server.
- Edits made over MCP do not appear in agent edit review.
- OAuth. ChatGPT's and Claude's ordinary connectors need it and a public URL, so they
  cannot use a static token. Use the tunnel, or a client that can send a header.

The scope covers ZenNotes tools only: anything else that can read the vault's folder,
or that holds `ZENNOTES_AUTH_TOKEN`, is not restricted by it. It also goes by path. A hard link
inside a shared folder shows the file it points to, because nothing can tell a hard link from a
note. A hidden note's name can also show indirectly: creating a note with that name gets `name 2`.

## Upgrading and rolling back

Notes are plain files and there are no vault migrations, so upgrading means
running a newer server against the same vault and data directories, and rolling
back means running the older one again. Each server release pins one browser
bundle, so upgrading the server upgrades the web app with it.

The server does not check for updates itself. New versions are published on the
[releases page](https://github.com/ZenNotes/znserver/releases). To be told about
them, choose **Watch → Custom → Releases** on the GitHub repository.

### Upgrade with Docker Compose

```sh
docker compose pull
docker compose up -d
```

`pull` fetches whatever your `image:` tag now points to, and `up -d` recreates
the container only if the image changed. A minor tag such as `2.51` moves to
each new patch of that minor, so these two commands are the whole upgrade. To
move to a newer minor, or off an exact tag such as `2.51.0`, change `image:` in
your Compose file first. [Image tags](#image-tags) lists what each tag follows.

### Upgrade a `docker run` container

A container keeps the image it was created with, so pull the new image and
replace the container:

```sh
docker pull adibhanna/zennotes:2.51   # your tag, or a newer one
docker stop zennotes
docker rm zennotes
```

Then run the `docker run` command from [step 2](#2-run-the-container) again,
with the new tag if you changed it. The vault, the server config, and saved
sessions live in the mounted directories, so replacing the container loses
nothing.

### Upgrade a prebuilt binary

Download and verify the new release with the commands under
[Prebuilt binary](#prebuilt-binary), with `VERSION` set to the new release. Then
install it over the old file and restart the server. For the systemd service on
Linux:

```sh
sudo install -m 0755 "zennotes-server-linux-${ARCH}" /usr/local/bin/zennotes-server
sudo systemctl restart zennotes
```

On macOS, install `zennotes-server-darwin-${ARCH}` the same way, then restart
the server however you run it. On Windows, stop the server first, since a
running `.exe` cannot be replaced, then put the new `zennotes-server.exe` in
place of the old one and start it again.

### Upgrade a source or Nix build

Check out the release you want:

```sh
git fetch --tags
git checkout vX.Y.Z   # the release to run
```

Then repeat the build under [Build from source](#build-from-source), or run
`nix-build`, and restart the server. An API-only `go install …@latest` build
follows `main`, not releases. Run the same `go install` command again to
update it.

### Check the running version

Published Docker images carry their version in a label. Ask the container
rather than the tag, since a container runs the image it was created with until
it is replaced:

```sh
docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' zennotes
```

With Compose, use `$(docker compose ps -q zennotes)` in place of `zennotes`.
Images you build yourself carry no version label.

The server also logs its version first on startup, as `ZenNotes Server: vX.Y.Z`
(see `docker compose logs zennotes` or `journalctl -u zennotes`), and reports it
without a token at `/api/version`, after your base path if you set one:

```sh
curl http://127.0.0.1:7878/api/version
```

Older servers log no version line and answer `0.1.0-web` there. A build from
`main` between releases reports the most recent release, not its own commit.

### Roll back

Run the previous version the same way you upgraded: its exact image tag, such
as `2.51.0`, in your Compose file or `docker run` command, or its release
binary. Nothing in the vault needs to be undone.

## Development

Go 1.26 or newer is enough for API work. No frontend toolchain is needed.

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
6. Once both are out, point this README's install commands at the new release:
   `VERSION` in the binary instructions and, when the minor changed, the image
   tags in the Docker examples.

## License

[MIT](LICENSE)
