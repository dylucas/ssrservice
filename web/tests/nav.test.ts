import { readFileSync } from 'node:fs'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'
import { appTitle, matchNav, navItems, titleOf } from '../src/nav'

// The sidebar and the document title are both generated from src/nav.ts. These
// checks cover the ways that shared list can go wrong without anyone noticing:
// an entry whose icon name AppLayout cannot resolve renders an empty menu row, a
// path with no route in router.ts is a dead menu item, and a wrong prefix match
// would highlight the wrong section.

const srcDir = fileURLToPath(new URL('../src', import.meta.url))
const layoutSource = readFileSync(`${srcDir}/components/AppLayout.vue`, 'utf8')
const routerSource = readFileSync(`${srcDir}/router.ts`, 'utf8')

/**
 * iconMapBody returns the body of AppLayout's `const icons = { ... }` map.
 *
 * The check below has to read the map, not the whole file: an icon that is
 * imported (so its name appears in the import block) but forgotten in the map
 * still renders an empty row, and a source-wide substring search cannot tell the
 * two apart. That is exactly how the 访问分析 entry shipped without an icon.
 */
function iconMapBody(): string {
  const start = layoutSource.indexOf('const icons: Record<string, Component> = {')
  expect(start, 'the icon map is not declared the way the guard expects').toBeGreaterThan(-1)
  const end = layoutSource.indexOf('\n}', start)
  expect(end, 'the icon map has no closing brace').toBeGreaterThan(start)
  return layoutSource.slice(start, end)
}

describe('navigation data', () => {
  it('has one entry per menu path', () => {
    const paths = navItems.map((item) => item.path)
    expect(new Set(paths).size, `duplicate paths: ${paths.join(', ')}`).toBe(paths.length)
  })

  it('gives every entry a title and an icon', () => {
    const map = iconMapBody()
    for (const item of navItems) {
      expect(item.title.length, `${item.path} has no title`).toBeGreaterThan(0)
      // AppLayout resolves the icon by name from an explicit map, and the map
      // only works for names it also imported. Both halves are checked here: a
      // name missing from the map renders an empty <el-icon>, and a name missing
      // from the import block would not compile but is worth pinning too.
      expect(
        new RegExp(`(^|\\s)${item.icon},`, 'm').test(map),
        `${item.icon} (${item.path}) is missing from AppLayout's icon map`,
      ).toBe(true)
      expect(
        new RegExp(`(^|\\s)${item.icon},`, 'm').test(layoutSource.slice(0, layoutSource.indexOf('const icons'))),
        `${item.icon} (${item.path}) is not imported by AppLayout`,
      ).toBe(true)
    }
  })

  it('does not keep icons the menu never uses', () => {
    // A leftover entry is dead weight in the entry chunk and hides the fact that
    // a renamed menu item was not cleaned up.
    const map = iconMapBody()
    const used = new Set(navItems.map((item) => item.icon))
    const declared = [...map.matchAll(/^\s{2}([A-Za-z][A-Za-z0-9]*),$/gm)].map((m) => m[1])
    expect(declared.length, 'the icon map could not be parsed').toBeGreaterThan(0)
    for (const name of declared) {
      expect(used.has(name), `${name} is in the icon map but no menu entry uses it`).toBe(true)
    }
    for (const name of used) {
      expect(declared, `${name} is used by the menu but not declared in the map`).toContain(name)
    }
  })

  it('points at routes that exist', () => {
    for (const item of navItems) {
      if (item.path === '/') {
        expect(routerSource, 'the dashboard route is missing').toContain("path: ''")
        continue
      }
      const segment = item.path.replace(/^\//, '')
      expect(routerSource, `${item.path} has no route`).toContain(`path: '${segment}'`)
    }
  })

  it('resolves the section for a path', () => {
    expect(titleOf('/')).toBe('仪表盘')
    expect(titleOf('/clients')).toBe('用户管理')
    expect(titleOf('/clients/deep')).toBe('用户管理')
    expect(titleOf('/nope')).toBe('')
    // The root path is only a prefix of itself; otherwise every page would
    // report 仪表盘.
    expect(titleOf('/inbounds')).toBe('入站管理')
    expect(matchNav('/nope')).toBeUndefined()
  })

  it('names the landing page as the default title', () => {
    expect(appTitle).toContain('sspanel')
    expect(routerSource, 'document.title is not kept in step').toContain('document.title')
  })
})
