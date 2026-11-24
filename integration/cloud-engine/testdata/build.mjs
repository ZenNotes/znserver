// Bundles engine.test.ts with the sync engine taken straight from a ZenNotes
// checkout (ZENNOTES_SRC): shared-domain's CloudSyncCoordinator and the
// desktop's DesktopCloudSyncRepository, the code a desktop offline copy runs.
// Nothing is copied or reimplemented, so the test fails when either side
// drifts. The engine needs no npm packages of its own; esbuild resolves the
// workspace imports to the checkout's sources.
import { build } from 'esbuild'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const source = process.env.ZENNOTES_SRC
if (!source) {
  console.error('Set ZENNOTES_SRC to a ZenNotes/zennotes checkout (the commit in zennotes-ref).')
  process.exit(2)
}
const root = path.resolve(source)
const desktop = path.join(root, 'apps', 'desktop', 'src', 'main')
if (!existsSync(path.join(desktop, 'cloud-sync-filesystem.ts'))) {
  console.error(`${root} is not a ZenNotes checkout: apps/desktop/src/main/cloud-sync-filesystem.ts is missing.`)
  process.exit(2)
}

await build({
  entryPoints: [path.join(here, 'engine.test.ts')],
  outfile: path.join(here, 'dist', 'engine.test.mjs'),
  bundle: true,
  platform: 'node',
  format: 'esm',
  target: 'node22',
  sourcemap: 'inline',
  logLevel: 'warning',
  alias: {
    '@zennotes/shared-domain': path.join(root, 'packages', 'shared-domain', 'src'),
    '@zennotes/bridge-contract': path.join(root, 'packages', 'bridge-contract', 'src'),
    '@zennotes-desktop': desktop
  }
})
