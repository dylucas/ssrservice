import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'

// Style guards.
//
// These tests do not render anything: they read the CSS and the component
// sources and check the invariants that would otherwise erode silently as the
// panel grows:
//   1. every colour and spacing step lives in the token block of styles.css,
//   2. component <style> blocks reference tokens instead of literals,
//   3. Element Plus is themed through those same tokens,
//   4. the text and semantic colours actually meet WCAG AA,
//   5. shared utility classes are defined once,
//   6. templates carry no inline styles and no fixed dialog widths.
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
    // A shared utility may carry an `html ` prefix when it has to outrank a
    // library rule (el-select sets its own width, for instance). That prefix is
    // part of the definition, not a second one, so it is stripped here.
    const shared = [...baseRules.matchAll(/^(?:html )?\.([a-zA-Z][\w-]*)\s*\{/gm)]
      .map((match) => match[1])
      // `el-*` selectors are Element Plus' own; the rules that mention them are
      // library overrides, not utilities of ours, and they legitimately repeat
      // the class to reach different inner elements.
      .filter((name) => !name.startsWith('el-'))
      .map((name) => `.${name}`)
    expect(shared.length).toBeGreaterThan(10)
    for (const className of shared) {
      // Count standalone definitions only: `.toolbar {` or `html .toolbar {`.
      // A deliberately scoped combo such as `html .el-date-editor.w-control-lg`
      // is an override of one library control, not a second definition of the
      // utility, and the anchored pattern is what tells the two apart.
      // className already carries its leading dot; the backslash escapes it.
      const definition = new RegExp(`^(?:html )?\\${className}\\s*\\{`, 'gm')
      const matches = baseRules.match(definition) ?? []
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
    [...css.matchAll(/^(?:html )?\.([a-zA-Z][\w-]*)\s*\{/gm)].map((match) => match[1]),
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
      // Dynamic bindings (`:class="[...]"`) are expressions, not class lists;
      // the negative lookbehind keeps them out of this check.
      for (const match of source.matchAll(/(?<![:\w-])class="([^"]+)"/g)) {
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

  it('does not use inline styles in templates', () => {
    // Inline styles escape the token system entirely. The previous version of
    // this check allowed width: 100% as a special case; .w-full now exists, so
    // the exception is gone and nothing is allowed through.
    const offenders: string[] = []
    for (const file of vueFiles) {
      const source = readFileSync(file, 'utf8')
      for (const match of source.matchAll(/^.*\bstyle="/gm)) {
        offenders.push(`${relative(srcDir, file)}: ${match[0].trim().slice(0, 80)}`)
      }
    }
    expect(
      offenders,
      `use a utility class instead of inline styles: ${offenders.join('; ')}`,
    ).toEqual([])
  })

  it('keeps margins, padding and gaps on the spacing scale', () => {
    // The type and radius checks below cover font-size/border-radius; without
    // this one, spacing was the last place a literal could hide (an 8px gap and
    // a 12px gap for the same relationship is exactly what "not unified" looks
    // like). Zero, auto, percentages and calc() are not steps on the scale, so
    // they are allowed.
    const allowed = /^(0|auto|inherit|0?\.\d+[a-z]*|[-\d.]+%|calc\(.*\)|var\(--sspanel-[a-z0-9-]+\))$/
    const property = /^(margin|margin-[a-z]+|padding|padding-[a-z]+|gap|row-gap|column-gap)$/
    const offenders: string[] = []

    function scan(text: string, label: string) {
      for (const match of text.matchAll(/([a-z-]+)\s*:\s*([^;{}]+);/g)) {
        const [, name, rawValue] = match
        if (!property.test(name)) continue
        // Shorthands carry several values ("0 12px"); each must be on the scale.
        for (const value of rawValue.trim().split(/\s+(?![^(]*\))/)) {
          if (allowed.test(value)) continue
          offenders.push(`${label}: ${name}: ${rawValue.trim()}`)
          break
        }
      }
    }

    scan(outsideTokenBlock(css), 'styles.css')
    for (const file of vueFiles) {
      const source = readFileSync(file, 'utf8')
      const styleStart = source.indexOf('<style')
      if (styleStart < 0) continue
      scan(source.slice(styleStart, source.indexOf('</style>', styleStart)), relative(srcDir, file))
    }
    expect(offenders, `use the spacing tokens: ${offenders.join('; ')}`).toEqual([])
  })
})

describe('element plus bridge', () => {
  // Mapping the library onto the design tokens is what stops a dialog title,
  // a table header and a page title from being three different greys. The check
  // is on the token block, because a component-local --el-* override would not
  // reach the components that read the variable from :root.
  const required = [
    '--el-font-family',
    '--el-font-size-base',
    '--el-text-color-regular',
    '--el-text-color-placeholder',
    '--el-border-color',
    '--el-fill-color-light',
    '--el-bg-color',
    '--el-box-shadow',
    '--el-border-radius-base',
    '--el-color-success',
    '--el-color-warning',
    '--el-color-danger',
    '--el-color-error',
    '--el-color-info',
  ]

  it('maps the library variables onto the design tokens', () => {
    const block = tokenBlock(css)
    for (const name of required) {
      const match = block.match(new RegExp(`${name}\\s*:\\s*([^;]+);`))
      expect(match, `${name} is not mapped in :root`).not.toBeNull()
      expect(match![1], `${name} should point at a --sspanel- token`).toContain('var(--sspanel-')
    }
  })

  it('prefixes the control-width utilities so el-select cannot stretch them', () => {
    // el-select declares `width: var(--el-select-width)` (100%) on itself at the
    // same specificity as a bare utility class, and its stylesheet is appended
    // after this file. A bare `.w-control-md` therefore does nothing and every
    // select fills its row - which is exactly the state this check was added to
    // prevent from coming back.
    for (const utility of ['w-control-sm', 'w-control-md', 'w-control-lg']) {
      expect(css, `html .${utility} is missing its prefix`).toContain(`html .${utility} {`)
      expect(
        css,
        `.${utility} must not also exist without the prefix`,
      ).not.toMatch(new RegExp(`^\\.${utility}\\s*\\{`, 'm'))
    }
  })

  it('overrides component-level variables with a higher-specificity selector', () => {
    // Element declares --el-table-*, --el-dialog-*, --el-pagination-* and
    // --el-tag-* on the component itself. The page-specific components ship in a
    // lazily loaded chunk that is appended after styles.css, so an equal
    // specificity rule here loses on source order. The `html` prefix is the
    // whole mechanism; deleting it silently reverts the table to Element's
    // greys, which is why it is asserted instead of left to a comment.
    for (const selector of ['html .el-table', 'html .el-dialog', 'html .el-pagination']) {
      expect(css, `${selector} override is missing`).toContain(`${selector} {`)
    }
    const tableBlock = css.slice(css.indexOf('html .el-table {'), css.indexOf('}', css.indexOf('html .el-table {')))
    expect(tableBlock).toContain('--el-table-header-bg-color: var(--sspanel-')
  })
})

describe('text contrast (WCAG AA)', () => {
  const cases: Array<{ name: string; token: string; background: string; min: number }> = [
    // Body and normal sized text on the page surfaces must reach 4.5:1.
    { name: 'body text', token: '--sspanel-text', background: '--sspanel-bg', min: 7 },
    { name: 'body text on card', token: '--sspanel-text', background: '--sspanel-surface', min: 7 },
    { name: 'muted text', token: '--sspanel-text-muted', background: '--sspanel-surface', min: 4.5 },
    { name: 'muted text on page', token: '--sspanel-text-muted', background: '--sspanel-bg', min: 4.5 },
    { name: 'muted text on the table header', token: '--sspanel-text-muted', background: '--sspanel-surface-sunken', min: 4.5 },
    { name: 'hint text', token: '--sspanel-text-hint', background: '--sspanel-surface', min: 4.5 },
    { name: 'aside text', token: '--sspanel-aside-text', background: '--sspanel-aside-bg', min: 4.5 },
    { name: 'aside muted text', token: '--sspanel-aside-muted', background: '--sspanel-aside-bg', min: 4.5 },
    // The sidebar's two interaction states carry text too: a 12px version string
    // or a section label must stay readable while hovered and while active.
    { name: 'aside muted text on hover', token: '--sspanel-aside-muted', background: '--sspanel-aside-hover', min: 4.5 },
    { name: 'aside text on the active item', token: '--sspanel-aside-text', background: '--sspanel-aside-active', min: 4.5 },
    // The avatar monogram is 12px text sitting on the primary tint.
    { name: 'primary text on the tinted surface', token: '--sspanel-primary-strong', background: '--sspanel-primary-tint', min: 4.5 },
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

  it('keeps the semantic colours readable on their own tinted background', () => {
    // A status tag is 12px text on the semantic colour's light-9 tint, and an
    // alert puts the same pair together. Element's defaults only reach 2.04-2.80
    // there, so the four families are ours and this is the check that keeps them
    // readable if someone re-tints one by hand.
    const families = ['success', 'warning', 'danger', 'info']
    for (const family of families) {
      const base = resolveToken(tokens, `--sspanel-${family}`)
      const tint = resolveToken(tokens, `--sspanel-${family}-light-9`)
      const ratio = contrastRatio(base, tint)
      expect(ratio, `${family}: ${base} on ${tint} = ${ratio.toFixed(2)}:1`).toBeGreaterThanOrEqual(
        4.5,
      )
      // White on the base is the "solid" tag and the primary button pairing.
      const onBase = contrastRatio('#ffffff', base)
      expect(onBase, `white on ${family} ${base} = ${onBase.toFixed(2)}:1`).toBeGreaterThanOrEqual(
        4.5,
      )
    }
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

  it('lets the settings page use the width it has', () => {
    // A single 640px column in a 1440px viewport left 46% of the page empty.
    // The layout is a card grid that adds a column when there is room, so the
    // guard checks for the grid rather than for a pixel width.
    // Read the block itself: the same declaration also appears on
    // .settings-grid--single, so a whole-file substring match would pass even
    // with the main rule capped at a fixed width.
    const gridBlock = css.slice(css.indexOf('.settings-grid {'))
    const gridBlockEnd = gridBlock.indexOf('}')
    const gridRule = gridBlock.slice(0, gridBlockEnd)
    expect(gridRule, 'settings must lay its cards out in a grid').toContain('display: grid')
    expect(
      gridRule,
      'the single-column case must fill its container, not a fixed width',
    ).toContain('grid-template-columns: minmax(0, 1fr)')
    // And it must never go back to a fixed narrow column.
    expect(css, 'form-narrow was removed; do not cap settings at a fixed width').not.toContain(
      '.form-narrow',
    )
    // Two columns are added at a width, not capped away from one.
    expect(css).toMatch(/\.settings-grid \{[^}]*grid-template-columns: repeat\(2/)
  })

  it('keeps the row-action buttons compact enough to stay on one line', () => {
    // Element pads a text button by 11px per side and only spaces *adjacent*
    // buttons, so at its defaults "链接 趋势 编辑 更多" needs 237px against a
    // 240px column minus 24px of cell padding - the dropdown wrapper wraps onto
    // a second line and the row looks cramped. The override is what buys the
    // room, so its absence is a silent regression rather than a build failure.
    expect(
      css,
      'in-cell text buttons need the tighter padding (see .row-more notes)',
    ).toContain('html .el-table .cell .el-button.is-text')

    // The "more" wrapper replaces the margin Element would have added between
    // two buttons, so its gap must equal that margin (12px = space-5).
    const block = css.slice(css.indexOf('.row-more {'), css.indexOf('}', css.indexOf('.row-more {')))
    expect(block, '.row-more must use the same gap as .el-button + .el-button').toContain(
      'var(--sspanel-space-5)',
    )
    const tokensBlock = tokenBlock(css)
    expect(tokensBlock, 'space-5 is the 12px step Element uses').toContain('--sspanel-space-5: 12px')
  })

  it('switches layout at the breakpoint the script uses', () => {
    // AppLayout collapses the sidebar at MOBILE_BREAKPOINT; the stylesheet
    // restates the number in its media queries. If they drift, the JS layout and
    // the CSS layout change at different widths and the page ends up half
    // compact. Reading the constant out of the source keeps this test from
    // importing a module that touches window.innerWidth at import time.
    const source = readFileSync(join(srcDir, 'breakpoints.ts'), 'utf8')
    const declared = Number(source.match(/MOBILE_BREAKPOINT\s*=\s*(\d+)/)?.[1])
    expect(declared, 'MOBILE_BREAKPOINT is not a number').toBeGreaterThan(0)

    const breakpoints = [...css.matchAll(/@media \(max-width: (\d+)px\)/g)].map((m) => Number(m[1]))
    expect(breakpoints.length, 'styles.css declares no breakpoint').toBeGreaterThan(0)
    expect(
      [...new Set(breakpoints)],
      'every media query must use the same breakpoint as MOBILE_BREAKPOINT',
    ).toEqual([declared])
  })
})
