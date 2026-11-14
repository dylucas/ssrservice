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

describe('navigation data', () => {
  it('has one entry per menu path', () => {
    const paths = navItems.map((item) => item.path)
    expect(new Set(paths).size, `duplicate paths: ${paths.join(', ')}`).toBe(paths.length)
  })

  it('gives every entry a title and an icon', () => {
    for (const item of navItems) {
      expect(item.title.length, `${item.path} has no title`).toBeGreaterThan(0)
      // AppLayout resolves the icon by name from an explicit map. A name that is
      // not imported there yields an empty <el-icon> and an unstyled menu row.
      expect(
        layoutSource.includes(`  ${item.icon},`),
        `${item.icon} is not in AppLayout's icon map`,
      ).toBe(true)
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
