import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  elementComponents,
  elementPlugins,
  elementServices,
  elementStyleComponents,
} from '../src/element-components'

// The panel registers Element Plus per component (src/element.ts) instead of
// loading the whole library. These tests guard that setup statically: they do
// not import element-plus, so they stay fast and do not pull the bundle in.
//
// They catch the two ways that setup can silently break the UI:
//   1. a template starts using an `el-*` tag that was never registered, and
//   2. a registered component is missing its style sheet import.

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
    const registered = new Set<string>(elementComponents)
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
      .filter(([name]) => !registered.has(name))
      .map(([name, files]) => `${name} (used in ${[...files].join(', ')})`)
    expect(missing, `register these in src/element-components.ts: ${missing.join('; ')}`).toEqual(
      [],
    )
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
    expect(missing, `missing style imports for: ${missing.join(', ')}`).toEqual([])
  })

  it('imports every registered component from element-plus', () => {
    const elementSource = readFileSync(join(srcDir, 'element.ts'), 'utf8')
    const missing = [...elementComponents, ...elementPlugins].filter(
      (name) => !new RegExp(`\\b${name}\\b`).test(elementSource),
    )
    expect(missing, `not imported in element.ts: ${missing.join(', ')}`).toEqual([])
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
