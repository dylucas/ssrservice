import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

// Bundle guards.
//
// The dashboard used to pull a canvas charting library (497 kB, 167 kB gzip) to
// draw two polylines, which was 41% of its landing page. Swapping it for inline
// SVG was mostly a deletion, and deletions regrow: these static checks fail if
// the library or a similar heavyweight import comes back. They read sources
// instead of building, so they stay fast and need no bundle output.

const srcDir = fileURLToPath(new URL('../src', import.meta.url))
const packageJson = JSON.parse(
  readFileSync(fileURLToPath(new URL('../package.json', import.meta.url)), 'utf8'),
) as { dependencies?: Record<string, string>; devDependencies?: Record<string, string> }

function walk(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) {
      out.push(...walk(full))
      continue
    }
    out.push(full)
  }
  return out
}

function sources(): { path: string; text: string }[] {
  return walk(srcDir)
    .filter((path) => path.endsWith('.ts') || path.endsWith('.vue'))
    .map((path) => ({ path, text: readFileSync(path, 'utf8') }))
}

describe('chart dependency', () => {
  it('does not import a charting library', () => {
    const offenders = sources()
      .filter((source) => /from\s+['"]echarts|import\(['"]echarts|require\(['"]echarts/.test(source.text))
      .map((source) => relative(srcDir, source.path))
    expect(offenders, `charts are inline SVG; drop the library import in: ${offenders.join(', ')}`).toEqual([])
  })

  it('does not declare one as a dependency', () => {
    const declared = { ...packageJson.dependencies, ...packageJson.devDependencies }
    for (const heavy of ['echarts', 'chart.js', 'd3', 'highcharts', 'apexcharts']) {
      expect(declared, `${heavy} must not be a dependency`).not.toHaveProperty(heavy)
    }
  })

  it('keeps the chart geometry module dependency free', () => {
    // utils/chart.ts is the part that can be unit tested without a DOM. An import
    // would mean it is no longer pure geometry, and the tests above would start
    // needing a renderer.
    const text = readFileSync(join(srcDir, 'utils', 'chart.ts'), 'utf8')
    expect(text).not.toMatch(/^\s*import\s/m)
    expect(text).not.toMatch(/\brequire\(/)
  })

  it('has no stale reference to the removed chart helper', () => {
    const offenders = sources()
      .filter((source) => /utils\/charts\b/.test(source.text))
      .map((source) => relative(srcDir, source.path))
    expect(offenders, `utils/charts.ts was deleted: ${offenders.join(', ')}`).toEqual([])
  })
})

describe('heavyweight optional modules', () => {
  it('loads the QR encoder on demand', () => {
    // 26 kB of encoder for two dialogs should not ride along in the route chunk
    // that the client list or settings page downloads. The encoder lives in one
    // shared module so both dialogs hit the same lazily loaded import, and no
    // source may pull it in statically.
    const shared = readFileSync(join(srcDir, 'utils', 'qr.ts'), 'utf8')
    expect(shared, 'qrcode must be a dynamic import').toMatch(/import\(['"]qrcode['"]\)/)

    const offenders = sources()
      .filter((source) =>
        /(?:^|\n)\s*import\s+(?:(?:type\s+)?[^;\n]*from\s+)?['"]qrcode['"]/.test(source.text),
      )
      .map((source) => relative(srcDir, source.path))
    expect(offenders, `qrcode must be loaded on demand: ${offenders.join(', ')}`).toEqual([])
  })
})
