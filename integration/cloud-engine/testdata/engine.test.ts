// ZenNotes Cloud's sync engine against a real znserver (#1108, phases 2 and 4).
//
// Two "devices" keep mirrors of one server vault with CloudSyncCoordinator and
// DesktopCloudSyncRepository, the exact code a desktop offline copy will run
// (bundled from a ZenNotes checkout by build.mjs). The remote below is the
// smallest adapter that speaks /api/sync/*; phase 3's ZnServerSyncRemote is
// that adapter plus the app's transport.
//
// Run through ../engine_test.go, or by hand:
//   ZENNOTES_SRC=~/zennotes ZENNOTES_SERVER_BINARY=/path/zennotes-server npm test

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import { createHash } from 'node:crypto'
import { promises as fs } from 'node:fs'
import { createServer } from 'node:net'
import os from 'node:os'
import path from 'node:path'
import type { CloudSyncMutation, CloudSyncMutationRequest } from '@zennotes/bridge-contract/cloud-sync'
import type { CloudSyncCoordinator, CloudSyncRemote } from '@zennotes/shared-domain/cloud-sync-coordinator'
import { shouldSyncVaultPath } from '@zennotes/shared-domain/cloud-sync'
import { createDesktopCloudSyncCoordinator } from '@zennotes-desktop/cloud-sync-filesystem'

const BINARY = process.env.ZENNOTES_SERVER_BINARY
const TOKEN = 'cloud-engine-test-token'
const TEXT = new Set(['.md', '.json', '.excalidraw', '.txt', '.csv', '.svg', '.base', '.yaml', '.yml', '.toml'])

/** What the desktop's Cloud client throws, as far as the engine reads it. */
class ServerRequestError extends Error {
  override name = 'CloudServiceRequestError'
  constructor(
    message: string,
    readonly status: number,
    readonly code: string | null,
    readonly details: Record<string, unknown> | null
  ) {
    super(message)
  }
}

async function freePort(): Promise<number> {
  const server = createServer()
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  server.close()
  await once(server, 'close')
  if (!address || typeof address === 'string') throw new Error('no port')
  return address.port
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

/** A rename on Windows fails while another process has the file open, as the
 * server briefly does when it hashes it; a person retries, so this does. */
async function renameRetrying(from: string, to: string): Promise<void> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await fs.rename(from, to)
    } catch (error) {
      const code = (error as NodeJS.ErrnoException).code
      if (attempt >= 40 || (code !== 'EPERM' && code !== 'EBUSY' && code !== 'EACCES')) throw error
      await sleep(50)
    }
  }
}

class TestServer {
  child: ChildProcess | null = null
  log = ''
  constructor(readonly root: string, readonly configPath: string, readonly port: number) {}

  url(route: string): string {
    return `http://127.0.0.1:${this.port}${route}`
  }

  async start(env: Record<string, string> = {}): Promise<void> {
    if (!BINARY) throw new Error('ZENNOTES_SERVER_BINARY is not set')
    const child = spawn(BINARY, [], {
      env: {
        ...process.env,
        ZENNOTES_VAULT_PATH: this.root,
        ZENNOTES_CONFIG_PATH: this.configPath,
        ZENNOTES_BIND: `127.0.0.1:${this.port}`,
        ZENNOTES_AUTH_TOKEN: TOKEN,
        ...env
      },
      stdio: ['ignore', 'pipe', 'pipe']
    })
    this.child = child
    child.stdout?.on('data', (chunk) => (this.log += chunk))
    child.stderr?.on('data', (chunk) => (this.log += chunk))
    for (let attempt = 0; attempt < 200; attempt++) {
      if (child.exitCode !== null) throw new Error(`server exited early:\n${this.log}`)
      try {
        const response = await fetch(this.url('/api/healthz'))
        if (response.ok) return
      } catch {
        // not up yet
      }
      await sleep(50)
    }
    throw new Error(`server did not come up:\n${this.log}`)
  }

  async stop(): Promise<void> {
    const child = this.child
    this.child = null
    if (!child || child.exitCode !== null) return
    child.kill()
    await once(child, 'exit')
  }

  async json<T>(method: 'GET' | 'POST', route: string, body?: unknown): Promise<T> {
    const response = await fetch(this.url(route), {
      method,
      headers: {
        Authorization: `Bearer ${TOKEN}`,
        Accept: 'application/json',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' })
      },
      body: body === undefined ? undefined : JSON.stringify(body)
    })
    const text = await response.text()
    let payload: Record<string, unknown> | null = null
    try {
      payload = JSON.parse(text) as Record<string, unknown>
    } catch {
      payload = null
    }
    if (!response.ok) {
      throw new ServerRequestError(
        typeof payload?.message === 'string' ? payload.message : text,
        response.status,
        typeof payload?.code === 'string' ? payload.code : null,
        payload?.errors ? { errors: payload.errors } : null
      )
    }
    return payload as T
  }
}

function query(values: Record<string, string | number | undefined>): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values)) {
    if (value !== undefined) params.set(key, String(value))
  }
  return `?${params}`
}

/** The hints the engine adds for encrypted vaults, which never go on the wire. */
function withoutHints(mutation: CloudSyncMutation): CloudSyncMutation {
  if (mutation.type !== 'move') return mutation
  const { kind: _kind, content_metadata: _metadata, ...wire } = mutation
  return wire
}

interface Seen {
  cursorExpired: number
  revisions: number
}

function remoteFor(server: TestServer, seen: Seen): CloudSyncRemote {
  return {
    // The manifest always carries content (the server sends it whatever
    // include_content says), so the engine never needs a revision read to
    // hydrate an item; it reads revisions for a conflict's base text.
    manifest: (vaultId, options) =>
      server.json('GET', `/api/sync/manifest${query({ vault_id: vaultId, page: options.page, perPage: options.perPage, include_content: 1 })}`),
    changes: async (vaultId, after, limit) => {
      try {
        return await server.json('GET', `/api/sync/changes${query({ vault_id: vaultId, after, limit })}`)
      } catch (error) {
        if (error instanceof ServerRequestError && error.code === 'CURSOR_EXPIRED') seen.cursorExpired++
        throw error
      }
    },
    // supportsSync 2. Any error (404 REVISION_NOT_RETAINED included) leaves
    // the conflict two-way, held for review.
    revision: (vaultId, itemId, revision) => {
      seen.revisions++
      return server.json('GET', `/api/sync/revision${query({ vault_id: vaultId, item: itemId, revision })}`)
    },
    mutate: (vaultId, body: CloudSyncMutationRequest) =>
      server.json('POST', `/api/sync/mutations${query({ vault_id: vaultId })}`, { mutations: body.mutations.map(withoutHints) })
  }
}

interface Device {
  name: string
  root: string
  seen: Seen
  coordinator: CloudSyncCoordinator
}

async function makeDevice(base: string, name: string, server: TestServer, vaultId: string): Promise<Device> {
  const root = path.join(base, name, 'vault')
  await fs.mkdir(root, { recursive: true })
  const seen = { cursorExpired: 0, revisions: 0 }
  const coordinator = createDesktopCloudSyncCoordinator({
    root,
    stateDirectory: path.join(base, name, 'state'),
    vaultId,
    remote: remoteFor(server, seen)
  })
  return { name, root, seen, coordinator }
}

async function write(root: string, rel: string, body: string | Buffer): Promise<void> {
  const target = path.join(root, ...rel.split('/'))
  await fs.mkdir(path.dirname(target), { recursive: true })
  await fs.writeFile(target, body)
}

async function read(root: string, rel: string): Promise<string | null> {
  try {
    return await fs.readFile(path.join(root, ...rel.split('/')), 'utf8')
  } catch {
    return null
  }
}

/** Every text file sync carries, by path, as a sha256. */
async function syncedTree(root: string): Promise<Record<string, string>> {
  const out: Record<string, string> = {}
  const walk = async (dir: string, prefix: string): Promise<void> => {
    for (const entry of await fs.readdir(dir, { withFileTypes: true })) {
      const rel = prefix ? `${prefix}/${entry.name}` : entry.name
      if (entry.isDirectory()) {
        if (rel !== '.zennotes/sync') await walk(path.join(dir, entry.name), rel)
      } else if (entry.isFile() && shouldSyncVaultPath(rel) && TEXT.has(path.extname(rel).toLowerCase())) {
        out[rel] = createHash('sha256').update(await fs.readFile(path.join(dir, entry.name))).digest('hex')
      }
    }
  }
  await walk(root, '')
  return out
}

async function itemIds(server: TestServer): Promise<Record<string, string>> {
  const ids: Record<string, string> = {}
  for (let page = 1; ; page++) {
    const response = await server.json<{ data: Array<{ path: string; item_id: string }>; next_page: number | null }>(
      'GET', `/api/sync/manifest${query({ page, perPage: 250 })}`
    )
    for (const item of response.data) ids[item.path] = item.item_id
    if (response.next_page === null) return ids
  }
}

async function eventually(check: () => Promise<boolean>, timeoutMs: number, what: string): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (await check()) return
    await sleep(250)
  }
  throw new Error(`timed out waiting for ${what}`)
}

test('Cloud sync engine keeps mirrors of a znserver vault', { timeout: 240_000 }, async (t) => {
  assert.ok(BINARY, 'set ZENNOTES_SERVER_BINARY')
  const base = await fs.mkdtemp(path.join(os.tmpdir(), 'zn-cloud-engine-'))
  const serverRoot = path.join(base, 'server-vault')
  const server = new TestServer(serverRoot, path.join(base, 'server.json'), await freePort())

  // The vault as a self-hoster has it before any copy exists.
  await write(serverRoot, 'inbox/Alpha.md', '# Alpha\n\nline one\nline two\nline three\n')
  await write(serverRoot, 'inbox/Source.md', '# Source\n')
  await write(serverRoot, 'inbox/Linker.md', 'Links to [[Source]].\n')
  await write(serverRoot, 'inbox/Projects/Plan.md', '# Plan\n\n- [ ] ship\n')
  await write(serverRoot, 'inbox/Doomed.md', '# Deleted offline\n')
  await write(serverRoot, 'inbox/Shared.md', '# Shared\n\nfirst line\nsecond line\nthird line\n')
  await write(serverRoot, 'assets/photo.png', Buffer.from([0x89, 0x50, 0x4e, 0x47, 0, 1, 2, 3, 0xff]))

  await server.start()
  let a: Device
  let b: Device
  let originalIds: Record<string, string>

  try {
    const { vault_id: vaultId } = await server.json<{ vault_id: string }>('GET', '/api/sync/manifest')
    assert.match(vaultId, /^[0-9a-f]{32}$/)
    a = await makeDevice(base, 'device-a', server, vaultId)
    b = await makeDevice(base, 'device-b', server, vaultId)
    originalIds = await itemIds(server)

    await t.test('bootstrap from the manifest', async () => {
      const result = await a.coordinator.sync()
      assert.equal(result.conflicts.length + result.pendingConflicts.length + result.bootstrapConflicts.length, 0)
      assert.ok(result.pulled >= 5, `pulled ${result.pulled}`)
      assert.equal(await read(a.root, 'inbox/Linker.md'), 'Links to [[Source]].\n')
      assert.deepEqual(await syncedTree(a.root), await syncedTree(serverRoot))
      assert.equal(await read(a.root, 'assets/photo.png'), null, 'attachments are not in the phase 2 copy')
      assert.equal(await read(a.root, '.zennotes/sync/vault_id'), null, 'the server store never syncs')
    })

    await t.test('offline edits are replayed when the server is back', async () => {
      await server.stop()
      await write(a.root, 'inbox/Alpha.md', '# Alpha\n\nline one\nline two\nline three\nline four (offline)\n')
      await write(a.root, 'inbox/Offline.md', '# Written offline\n')
      await fs.rename(path.join(a.root, 'inbox', 'Projects', 'Plan.md'), path.join(a.root, 'inbox', 'Projects', 'Plan v2.md'))
      await fs.rm(path.join(a.root, 'inbox', 'Doomed.md'))
      await assert.rejects(a.coordinator.sync(), 'no server, no sync')

      await server.start()
      const result = await a.coordinator.sync()
      assert.equal(result.conflicts.length, 0, JSON.stringify(result.conflicts))
      assert.equal(result.pushed, 4)
      assert.equal(await read(serverRoot, 'inbox/Alpha.md'), '# Alpha\n\nline one\nline two\nline three\nline four (offline)\n')
      assert.equal(await read(serverRoot, 'inbox/Offline.md'), '# Written offline\n')
      assert.equal(await read(serverRoot, 'inbox/Projects/Plan.md'), null)
      assert.equal(await read(serverRoot, 'inbox/Projects/Plan v2.md'), '# Plan\n\n- [ ] ship\n')
      assert.equal(await read(serverRoot, 'inbox/Doomed.md'), null)
      const ids = await itemIds(server)
      assert.equal(ids['inbox/Projects/Plan v2.md'], originalIds['inbox/Projects/Plan.md'], 'a rename keeps its item')
    })

    await t.test('a binary in the mirror is set aside, the rest syncs', async () => {
      await write(a.root, 'assets/offline.png', Buffer.from([0x89, 0x50, 0x4e, 0x47, 9, 9, 0xfe]))
      await write(a.root, 'inbox/Offline.md', '# Written offline\n\nand edited next to a picture\n')
      const result = await a.coordinator.sync()
      assert.deepEqual(result.refusedPaths.map((entry) => entry.path), ['assets/offline.png'])
      assert.equal(await read(serverRoot, 'assets/offline.png'), null)
      assert.equal(await read(serverRoot, 'inbox/Offline.md'), '# Written offline\n\nand edited next to a picture\n')
      const again = await a.coordinator.sync()
      assert.equal(again.pushed, 0, 'a held path is not offered again on every run')
    })

    await t.test('a second device bootstraps', async () => {
      await b.coordinator.sync()
      assert.deepEqual(await syncedTree(b.root), await syncedTree(serverRoot))
    })

    await t.test('a conflict is held for review, never written over', async () => {
      // The same line on both sides: no merge can settle it.
      await write(a.root, 'inbox/Alpha.md', '# Alpha\n\nline one (A)\nline two\nline three\nline four (offline)\n')
      await write(b.root, 'inbox/Alpha.md', '# Alpha\n\nline one (B)\nline two\nline three\nline four (offline)\n')
      const fromA = await a.coordinator.sync()
      assert.equal(fromA.pushed, 1)
      const fromB = await b.coordinator.sync()
      const pending = fromB.pendingConflicts.find((conflict) => conflict.path === 'inbox/Alpha.md')
      assert.ok(pending, `B holds the conflict: ${JSON.stringify(fromB)}`)
      assert.equal(await read(serverRoot, 'inbox/Alpha.md'), '# Alpha\n\nline one (A)\nline two\nline three\nline four (offline)\n',
        'the server keeps the version that got there first')
      assert.equal(await read(b.root, 'inbox/Alpha.md'), '# Alpha\n\nline one (B)\nline two\nline three\nline four (offline)\n',
        "B's edit stays on B")

      const details = await b.coordinator.getConflict(pending.id)
      assert.match(details.cloud.text ?? '', /line one \(A\)/)
      const merged = '# Alpha\n\nline one (A and B)\nline two\nline three\nline four (offline)\n'
      await b.coordinator.resolveConflict({
        conflict_id: pending.id,
        choice: 'merged',
        merged_text: merged,
        expected_local_sha256: details.local.sha256,
        expected_cloud_revision: details.cloud.revision
      })
      assert.equal(await read(serverRoot, 'inbox/Alpha.md'), merged)
      assert.equal(await read(b.root, 'inbox/Alpha.md'), merged)
      await a.coordinator.sync()
      assert.equal(await read(a.root, 'inbox/Alpha.md'), merged)
    })

    await t.test('edits to different lines of one note merge on their own', async () => {
      // Shared.md is revision 1 of a file the store started with, so its base
      // is read back after the server's edit has replaced it on disk.
      await a.coordinator.sync()
      await b.coordinator.sync()
      const before = a.seen.revisions
      const serverEdit = '# Shared\n\nfirst line (server)\nsecond line\nthird line\n'
      const localEdit = '# Shared\n\nfirst line\nsecond line\nthird line (A)\n'
      const merged = '# Shared\n\nfirst line (server)\nsecond line\nthird line (A)\n'
      await server.json('POST', '/api/notes/write', { path: 'inbox/Shared.md', body: serverEdit })
      await write(a.root, 'inbox/Shared.md', localEdit)

      const result = await a.coordinator.sync()
      assert.ok(a.seen.revisions > before, 'the engine read the base revision')
      assert.equal(result.conflicts.length + result.pendingConflicts.length + result.localConflicts.length, 0, JSON.stringify(result))
      assert.equal(await read(a.root, 'inbox/Shared.md'), merged, 'the mirror has both edits')
      assert.equal(await read(serverRoot, 'inbox/Shared.md'), merged, 'the server has both edits')
      const again = await a.coordinator.sync()
      assert.equal(again.pendingConflicts.length, 0)
      assert.equal(again.pushed, 0, 'nothing left to send')
      await b.coordinator.sync()
      assert.equal(await read(b.root, 'inbox/Shared.md'), merged)
    })

    await t.test('a rename on the server arrives as a move, links rewritten', async () => {
      const before = await itemIds(server)
      await server.json('POST', '/api/notes/rename', { path: 'inbox/Source.md', title: 'Renamed Source' })
      const result = await a.coordinator.sync()
      assert.equal(result.localConflicts.length + result.pendingConflicts.length, 0)
      assert.equal(await read(a.root, 'inbox/Source.md'), null)
      assert.equal(await read(a.root, 'inbox/Renamed Source.md'), '# Source\n')
      assert.equal(await read(a.root, 'inbox/Linker.md'), 'Links to [[Renamed Source]].\n')
      const after = await itemIds(server)
      assert.equal(after['inbox/Renamed Source.md'], before['inbox/Source.md'])
    })

    await t.test('deletes travel both ways, Empty Trash included', async () => {
      await b.coordinator.sync()
      await fs.rm(path.join(b.root, 'inbox', 'Offline.md'))
      await b.coordinator.sync()
      assert.equal(await read(serverRoot, 'inbox/Offline.md'), null)
      await a.coordinator.sync()
      assert.equal(await read(a.root, 'inbox/Offline.md'), null)

      const trashed = await server.json<{ path: string }>('POST', '/api/notes/trash', { path: 'inbox/Linker.md' })
      await a.coordinator.sync()
      assert.equal(await read(a.root, 'inbox/Linker.md'), null)
      assert.equal(await read(a.root, trashed.path), 'Links to [[Renamed Source]].\n', 'trash is a move')
      await server.json('POST', '/api/notes/empty-trash', {})
      await a.coordinator.sync()
      assert.equal(await read(a.root, trashed.path), null, 'Empty Trash is a delete')
    })

    await t.test('an edit made on the server disk is caught by reconcile', async () => {
      const before = await itemIds(server)
      const shellEdit = '# Alpha\n\nrewritten in a shell on the server\n'
      await write(serverRoot, 'inbox/Alpha.md', shellEdit)
      await renameRetrying(path.join(serverRoot, 'inbox', 'Renamed Source.md'), path.join(serverRoot, 'inbox', 'Hand Moved.md'))
      // The watcher's hint reconciles within seconds; the change feed's own
      // reconcile (every 15 s at most) is the upper bound.
      await eventually(async () => {
        await a.coordinator.sync()
        return (await read(a.root, 'inbox/Alpha.md')) === shellEdit &&
          (await read(a.root, 'inbox/Hand Moved.md')) === '# Source\n'
      }, 60_000, 'the shell edit to reach device A')
      assert.equal(await read(a.root, 'inbox/Renamed Source.md'), null)
      const after = await itemIds(server)
      assert.equal(after['inbox/Hand Moved.md'], before['inbox/Renamed Source.md'], 'a move by hand is a move')
    })

    await t.test('a cursor older than the log rebuilds from the manifest', async () => {
      await b.coordinator.sync()
      // B edits offline, then sits out while the log moves on and is pruned.
      await write(b.root, 'inbox/Projects/Plan v2.md', '# Plan\n\n- [x] ship (B, before the rebuild)\n')
      await write(a.root, 'inbox/After.md', '# Written while B was away\n')
      await a.coordinator.sync()
      await server.stop()
      await sleep(1500)
      await server.start({ ZENNOTES_SYNC_LOG_RETENTION: '1s' })
      await eventually(async () => {
        try {
          await server.json('GET', `/api/sync/changes${query({ vault_id: vaultId, after: 0 })}`)
          return false
        } catch (error) {
          return error instanceof ServerRequestError && error.status === 410
        }
      }, 20_000, 'the startup prune')

      const result = await b.coordinator.sync()
      assert.ok(b.seen.cursorExpired >= 1, 'B was told its cursor expired')
      assert.equal(result.pendingConflicts.length, 0, JSON.stringify(result.pendingConflicts))
      assert.equal(await read(b.root, 'inbox/After.md'), '# Written while B was away\n')
      assert.equal(await read(serverRoot, 'inbox/Projects/Plan v2.md'), '# Plan\n\n- [x] ship (B, before the rebuild)\n',
        "B's offline edit survives the rebuild")
    })

    await t.test('everyone converges', async () => {
      await a.coordinator.sync()
      await b.coordinator.sync()
      const serverTree = await syncedTree(serverRoot)
      assert.deepEqual(await syncedTree(a.root), serverTree)
      assert.deepEqual(await syncedTree(b.root), serverTree)
    })
  } catch (error) {
    console.error(server.log)
    throw error
  } finally {
    await server.stop()
    await fs.rm(base, { recursive: true, force: true }).catch(() => undefined)
  }
})
