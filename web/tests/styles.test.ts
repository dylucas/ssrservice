import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

// Style guards.
//
// These tests do not render anything: they read the CSS and the component
// sources and check the invariants that would otherwise erode silently as the
// panel grows:
//   1. every colour lives in the token block of styles.css,
//   2. component <style> blocks reference tokens instead of literals,
//   3. the text tokens actually meet WCAG AA against the surfaces they sit on,
//   4. shared utility classes are defined once.
// The comparison is a computed contrast ratio, so lightening a "muted" colour to
// taste fails here instead of shipping unreadable text.

const srcDir = fileURLToPath(new URL('../src', import.meta.url))
const stylesPath = join(srcDir, 'styles.css')

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

/** tokenBlock returns the text of the first :root block. */
function tokenBlock(css: string): string {
  const start = css.indexOf(':root {')
  expect(start, 'styles.css must open with a :root token block').toBeGreaterThanOrEqual(0)
  const end = css.indexOf('\n}', start)
  expect(end, 'the :root block must be closed').toBeGreaterThan(start)
  return css.slice(start, end)
}

/** outsideTokenBlock returns the css with the :root block removed. */
function outsideTokenBlock(css: string): string {
  const block = tokenBlock(css)
  return css.replace(block, '')
}

/** colorLiterals finds #rgb/#rrggbb values and rgb()/rgba() calls. */
function colorLiterals(text: string): string[] {
  const found: string[] = []
  for (const match of text.matchAll(/#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b|rgba?\(/g)) {
    found.push(match[0])
  }
  return found
}

/** parseTokens extracts custom properties from the :root block. */
function parseTokens(css: string): Map<string, string> {
  const tokens = new Map<string, string>()
  for (const match of tokenBlock(css).matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/gi)) {
    tokens.set(match[1], match[2].trim())
  }
  return tokens
}

/** resolveToken follows var() indirection to a literal colour. */
function resolveToken(tokens: Map<string, string>, name: string, depth = 0): string {
  expect(depth, `token ${name} has a var() cycle`).toBeLessThan(10)
  const raw = tokens.get(name)
  expect(raw, `token ${name} is not defined`).toBeTruthy()
  const reference = raw!.match(/^var\((--[a-z0-9-]+)\)$/i)
  if (reference) return resolveToken(tokens, reference[1], depth + 1)
  return raw!
}

function hexToRgb(hex: string): [number, number, number] {
  const value = hex.replace('#', '')
  const full =
    value.length === 3
      ? value
          .split('')
          .map((c) => c + c)
          .join('')
      : value
  return [
    parseInt(full.slice(0, 2), 16),
    parseInt(full.slice(2, 4), 16),
    parseInt(full.slice(4, 6), 16),
  ]
}

/** relativeLuminance implements the WCAG 2.x definition. */
function relativeLuminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map((channel) => {
    const value = channel / 255
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** contrastRatio returns the WCAG contrast ratio between two colours. */
function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a)
  const lb = relativeLuminance(b)
  const [hi, lo] = la > lb ? [la, lb] : [lb, la]
  return (hi + 0.05) / (lo + 0.05)
}

const css = readFileSync(stylesPath, 'utf8')
const tokens = parseTokens(css)

describe('design tokens', () => {
  it('keeps every colour literal inside the token block', () => {
    // rgba() shadows and the brand divider are the only allowed escapes, and they
    // are declared in :root like everything else.
    const literals = colorLiterals(outsideTokenBlock(css))
    expect(literals, `colours outside :root: ${literals.join(', ')}`).toEqual([])
  })

  it('does not hardcode colours in component style blocks', () => {
    const offenders: string[] = []
    for (const file of walk(srcDir).filter((path) => path.endsWith('.vue'))) {
      const source = readFileSync(file, 'utf8')
      const styleStart = source.indexOf('<style')
      if (styleStart < 0) continue
      const styleEnd = source.indexOf('</style>', styleStart)
      const style = source.slice(styleStart, styleEnd)
      const literals = colorLiterals(style)
      if (literals.length > 0) {
        offenders.push(`${relative(srcDir, file)}: ${literals.join(', ')}`)
      }
    }
    expect(offenders, `use design tokens instead: ${offenders.join('; ')}`).toEqual([])
  })

  it('defines each shared utility class once', () => {
    // Every class defined globally must not be redefined in a component. Deriving
    // the list from styles.css (instead of naming a few) is what would have caught
    // the duplicated .toolbar that shipped earlier.
    // Media-query blocks may legitimately restate a class (e.g. .page shrinks on
    // phones), so only the top-level declarations are counted here.
    const baseRules = css.replace(/@media[^{]*\{(?:[^{}]*\{[^{}]*\})*[^{}]*\}/g, '')
    const shared = [...baseRules.matchAll(/^\.([a-zA-Z][\w-]*)\s*\{/gm)].map(
      (match) => `.${match[1]}`,
    )
    expect(shared.length).toBeGreaterThan(10)
    for (const className of shared) {
      // Match "selector {" at any nesting depth, anchored to the class token.
      const pattern = new RegExp(`(?<![\\w-])\\${className}(?![\\w-])[^{}]*\\{`, 'g')
      const matches = baseRules.match(pattern) ?? []
      expect(matches.length, `${className} should be defined once in styles.css`).toBe(1)
      const componentRedefinitions: string[] = []
      for (const file of walk(srcDir).filter((path) => path.endsWith('.vue'))) {
        const source = readFileSync(file, 'utf8')
        const styleStart = source.indexOf('<style')
        if (styleStart < 0) continue
        const style = source.slice(styleStart, source.indexOf('</style>', styleStart))
        if (new RegExp(`(?<![\\w-])\\${className}(?![\\w-])[^{}]*\\{`).test(style)) {
          componentRedefinitions.push(relative(srcDir, file))
        }
      }
      expect(
        componentRedefinitions,
        `${className} is global; drop the copy in ${componentRedefinitions.join(', ')}`,
      ).toEqual([])
    }
  })
})

describe('template and token hygiene', () => {
  const vueFiles = walk(srcDir).filter((path) => path.endsWith('.vue'))
  const globalClasses = new Set(
    [...css.matchAll(/^\.([a-zA-Z][\w-]*)\s*\{/gm)].map((match) => match[1]),
  )

  it('defines every class a template uses', () => {
    // A renamed or mistyped class is invisible: the element simply loses its
    // styling. This caught .filters, which a previous refactor deleted from the
    // CSS while the template still referenced it.
    const offenders: string[] = []
    for (const file of vueFiles) {
      const source = readFileSync(file, 'utf8')
      const styleStart = source.indexOf('<style')
      const style = styleStart < 0 ? '' : source.slice(styleStart, source.indexOf('</style>', styleStart))
      const local = new Set(
        [...style.matchAll(/\.([a-zA-Z][\w-]*)\s*(?:,|\{)/g)].map((match) => match[1]),
      )
      for (const match of source.matchAll(/class="([^"]+)"/g)) {
        for (const token of match[1].split(/\s+/).filter(Boolean)) {
          // Element Plus owns el-* classes; dynamic bindings are not parsed here.
          if (token.startsWith('el-')) continue
          if (globalClasses.has(token) || local.has(token)) continue
          offenders.push(`${relative(srcDir, file)}: .${token}`)
        }
      }
    }
    expect(offenders, `class used but never styled: ${offenders.join('; ')}`).toEqual([])
  })

  it('keeps font sizes and radii on the scale', () => {
    // Everything below :root must reference a token, otherwise the type scale is
    // decorative rather than enforced.
    const offenders: string[] = []
    for (const rule of outsideTokenBlock(css).matchAll(/(font-size|border-radius)\s*:\s*([^;]+);/g)) {
      if (!rule[2].includes('var(')) offenders.push(`${rule[1]}: ${rule[2].trim()}`)
    }
    for (const file of vueFiles) {
      const source = readFileSync(file, 'utf8')
      const styleStart = source.indexOf('<style')
      if (styleStart < 0) continue
      const style = source.slice(styleStart, source.indexOf('</style>', styleStart))
      for (const rule of style.matchAll(/(font-size|border-radius)\s*:\s*([^;]+);/g)) {
        if (!rule[2].includes('var(')) {
          offenders.push(`${relative(srcDir, file)}: ${rule[1]}: ${rule[2].trim()}`)
        }
      }
    }
    expect(offenders, `use the type/radius tokens: ${offenders.join('; ')}`).toEqual([])
  })

  it('does not override spacing or control widths inline', () => {
    // Inline styles escape the token system entirely. width: 100% is allowed: it
    // expresses "fill the container", not a size on the scale.
    const offenders: string[] = []
    for (const file of vueFiles) {
      const source = readFileSync(file, 'utf8')
      for (const match of source.matchAll(/style="([^"]+)"/g)) {
        for (const declaration of match[1].split(';').map((part) => part.trim()).filter(Boolean)) {
          if (/^width\s*:\s*100%$/.test(declaration)) continue
          offenders.push(`${relative(srcDir, file)}: ${declaration}`)
        }
      }
    }
    expect(
      offenders,
      `use a utility class instead of inline styles: ${offenders.join('; ')}`,
    ).toEqual([])
  })
})

describe('text contrast (WCAG AA)', () => {
  const cases: Array<{ name: string; token: string; background: string; min: number }> = [
    // Body and normal sized text on the page surfaces must reach 4.5:1.
    { name: 'body text', token: '--sspanel-text', background: '--sspanel-bg', min: 7 },
    { name: 'body text on card', token: '--sspanel-text', background: '--sspanel-surface', min: 7 },
    { name: 'muted text', token: '--sspanel-text-muted', background: '--sspanel-surface', min: 4.5 },
    { name: 'muted text on page', token: '--sspanel-text-muted', background: '--sspanel-bg', min: 4.5 },
    { name: 'hint text', token: '--sspanel-text-hint', background: '--sspanel-surface', min: 4.5 },
    { name: 'aside text', token: '--sspanel-aside-text', background: '--sspanel-aside-bg', min: 4.5 },
    { name: 'aside muted text', token: '--sspanel-aside-muted', background: '--sspanel-aside-bg', min: 4.5 },
  ]

  for (const testCase of cases) {
    it(`${testCase.name} reaches ${testCase.min}:1`, () => {
      const foreground = resolveToken(tokens, testCase.token)
      const background = resolveToken(tokens, testCase.background)
      const ratio = contrastRatio(foreground, background)
      expect(
        ratio,
        `${testCase.name}: ${foreground} on ${background} = ${ratio.toFixed(2)}:1`,
      ).toBeGreaterThanOrEqual(testCase.min)
    })
  }

  it('primary button text is readable', () => {
    const primary = resolveToken(tokens, '--sspanel-primary')
    const ratio = contrastRatio('#ffffff', primary)
    // The Element Plus default (#409eff) only reaches 2.78:1, which is why the
    // token exists; anything below 4.5 fails normal sized button labels.
    expect(ratio, `white on ${primary} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(4.5)
  })

  it('exports the brand colour as the Element Plus primary', () => {
    // Guards against the two drifting apart: a primary that no longer matches the
    // token would leave buttons on a colour nobody checked the contrast of.
    const primary = resolveToken(tokens, '--sspanel-primary')
    const elementPrimary = resolveToken(tokens, '--el-color-primary')
    expect(elementPrimary).toBe(primary)
  })
})

describe('responsive rules', () => {
  it('adapts dialogs to narrow viewports', () => {
    const offenders: string[] = []
    for (const file of walk(srcDir).filter((path) => path.endsWith('.vue'))) {
      const source = readFileSync(file, 'utf8')
      // Only dialog widths matter here; label-width="..." on el-form is a
      // column width inside the dialog and stays fixed.
      for (const match of source.matchAll(/(?<!label-)width="(\d+)px"/g)) {
        offenders.push(`${relative(srcDir, file)}: width="${match[1]}px"`)
      }
    }
    expect(
      offenders,
      `use width="min(<size>px, 92vw)" so dialogs fit a phone: ${offenders.join('; ')}`,
    ).toEqual([])
  })

  it('shortens the chart and tightens the card on small screens', () => {
    const media = css.slice(css.indexOf('@media (max-width: 782px)'))
    expect(media).toContain('--sspanel-chart-height')
    expect(media).toContain('overflow-x: auto')
  })
})
