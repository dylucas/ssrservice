import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  elementComponents,
  elementParts,
  elementPlugins,
  elementServices,
  elementStyleComponents,
  partStyleComponents,
} from '../src/element-components'

// The panel registers Element Plus per component instead of loading the whole
// library, in two tiers: the application shell is registered globally in
// src/element.ts, and page-specific components are exported from
// src/element-parts.ts for the views that render them. These tests guard that
// setup statically: they do not import element-plus, so they stay fast and do
// not pull the bundle in.
//
// They catch the ways that setup can silently break the UI:
//   1. a template starts using an `el-*` tag that is in neither tier,
//   2. a registered component is missing its style sheet import,
//   3. a component is declared in a tier but not actually imported there, and
//   4. a view imports a component from element-parts that it does not render
//      (dead weight) or renders one it does not import (blank cell).

const srcDir = fileURLToPath(new URL('../src', import.meta.url))

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

/** kebabCaseComponent turns a template tag into the registered component name. */
function pascalCase(tag: string): string {
  return tag
    .split('-')
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join('')
}

/** kebabTag turns a registered component name back into its template tag. */
function kebabTag(name: string): string {
  return 'el-' + kebabCase(name).replace(/^el-/, '')
}

/** kebabCase turns a component name into its style directory name. */
function kebabCase(name: string): string {
  return name
    .replace(/^El/, '')
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .toLowerCase()
}

const sourceFiles = walk(srcDir).filter((file) => file.endsWith('.vue') || file.endsWith('.ts'))

function readAllSources(): { path: string; text: string }[] {
  return sourceFiles.map((path) => ({ path, text: readFileSync(path, 'utf8') }))
}

describe('element plus registration', () => {
  it('registers every el-* component used in templates', () => {
    // A component is available either because the shell registers it globally or
    // because the file that renders it imports it from element-parts.
    const known = new Set<string>([...elementComponents, ...elementParts])
    const used = new Map<string, Set<string>>()
    for (const { path, text } of readAllSources()) {
      // Only look at template markup: component tags are lowercase kebab-case.
      for (const match of text.matchAll(/<el-([a-z][a-z0-9-]*)/g)) {
        const name = pascalCase(`el-${match[1]}`)
        if (!used.has(name)) used.set(name, new Set())
        used.get(name)!.add(relative(srcDir, path))
      }
    }
    expect(used.size).toBeGreaterThan(0)
    const missing = [...used.entries()]
      .filter(([name]) => !known.has(name))
      .map(([name, files]) => `${name} (used in ${[...files].join(', ')})`)
    expect(missing, `register these in src/element-components.ts: ${missing.join('; ')}`).toEqual(
      [],
    )
  })

  it('imports every page-specific component from element-parts where it is used', () => {
    // A view that renders <el-table> but never imports ElTable would render
    // nothing at all; a view that imports a component it does not render just
    // carries dead weight into its chunk.
    const offenders: string[] = []
    for (const { path, text } of readAllSources()) {
      const rendered = new Set(
        [...text.matchAll(/<el-([a-z][a-z0-9-]*)/g)].map((m) => pascalCase(`el-${m[1]}`)),
      )
      const importedMatch = text.match(/import \{([^}]+)\} from '@\/element-parts'/)
      const imported = new Set(
        (importedMatch?.[1] ?? '')
          .split(',')
          .map((part) => part.trim())
          .filter(Boolean),
      )
      for (const name of rendered) {
        if (elementParts.includes(name as (typeof elementParts)[number]) && !imported.has(name)) {
          offenders.push(`${relative(srcDir, path)} renders <${kebabTag(name)}> but does not import ${name}`)
        }
      }
      for (const name of imported) {
        if (!rendered.has(name)) {
          offenders.push(`${relative(srcDir, path)} imports ${name} but never renders it`)
        }
      }
    }
    expect(offenders, offenders.join('; ')).toEqual([])
  })

  it('registers the element plus directives used in templates', () => {
    const text = readAllSources()
      .map((source) => source.text)
      .join('\n')
    if (/\bv-loading\b/.test(text)) {
      // v-loading is provided by the ElLoading plugin.
      expect(elementPlugins).toContain('ElLoading')
    }
    if (/\bv-infinite-scroll\b/.test(text)) {
      expect(elementPlugins).toContain('ElInfiniteScroll')
    }
    if (/\bv-popover\b/.test(text)) {
      expect(elementPlugins).toContain('ElPopoverDirective')
    }
  })

  it('imports the style sheet of every registered component', () => {
    const elementSource = readFileSync(join(srcDir, 'element.ts'), 'utf8')
    const missing = elementStyleComponents.filter(
      (name) => !elementSource.includes(`element-plus/es/components/${kebabCase(name)}/style/css`),
    )
    expect(missing, `missing style imports in element.ts: ${missing.join(', ')}`).toEqual([])
  })

  it('imports the style sheet of every page-specific component', () => {
    const partsSource = readFileSync(join(srcDir, 'element-parts.ts'), 'utf8')
    const missing = partStyleComponents.filter(
      (name) => !partsSource.includes(`element-plus/es/components/${kebabCase(name)}/style/css`),
    )
    expect(missing, `missing style imports in element-parts.ts: ${missing.join(', ')}`).toEqual([])
  })

  it('imports every registered component from element-plus', () => {
    // The registry object and the import block must agree: a name listed in one
    // but not the other is either dead weight or an undefined component at
    // runtime (which is how the two-tier split first broke: the registry kept
    // referencing components whose imports had moved).
    const elementSource = readFileSync(join(srcDir, 'element.ts'), 'utf8')
    const missing = [...elementComponents, ...elementPlugins].filter(
      (name) => !new RegExp(`\\b${name}\\b`).test(elementSource),
    )
    expect(missing, `not imported in element.ts: ${missing.join(', ')}`).toEqual([])

    const partsSource = readFileSync(join(srcDir, 'element-parts.ts'), 'utf8')
    const missingParts = elementParts.filter(
      (name) => !new RegExp(`\\b${name}\\b`).test(partsSource),
    )
    expect(missingParts, `not exported by element-parts.ts: ${missingParts.join(', ')}`).toEqual([])
  })

  it('keeps the page-specific tier out of the global registry', () => {
    // Registering a component globally puts it in the entry chunk, which is the
    // cost the split exists to avoid.
    const misplaced = elementParts.filter((name) =>
      (elementComponents as readonly string[]).includes(name),
    )
    expect(misplaced, `these are global and page-specific at once: ${misplaced.join(', ')}`).toEqual(
      [],
    )
  })

  it('does not load the full element-plus bundle', () => {
    const mainSource = readFileSync(join(srcDir, 'main.ts'), 'utf8')
    expect(mainSource).not.toMatch(/from ['"]element-plus['"]/)
    expect(mainSource).not.toMatch(/element-plus\/dist\/index\.css/)
    expect(mainSource).not.toMatch(/app\.use\(ElementPlus/)
    expect(mainSource).toMatch(/installElement\(app\)/)
    // Every view that calls ElMessage/ElMessageBox imports it by name.
    for (const service of elementServices) {
      const callers = readAllSources().filter((source) => source.text.includes(service))
      expect(callers.length, `${service} is not used anywhere`).toBeGreaterThan(0)
    }
  })
})
