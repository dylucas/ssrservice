// Navigation data shared by the sidebar (components/AppLayout.vue) and the
// document title (router.ts).
//
// It is deliberately plain data with no imports: AppLayout renders the icons and
// owns the name-to-component mapping, so this module can be read by the route
// setup and by the tests without pulling the icon set into the entry chunk twice.

export interface NavItem {
  /** Route path, as registered in router.ts. */
  path: string
  /** Sidebar label and the page name in a document title. */
  title: string
  /** Icon name, resolved by AppLayout's icon map. */
  icon: string
}

/** Menu order. The first entry is the landing page. */
export const navItems: readonly NavItem[] = [
  { path: '/', title: '仪表盘', icon: 'Odometer' },
  { path: '/inbounds', title: '入站管理', icon: 'Connection' },
  { path: '/clients', title: '用户管理', icon: 'User' },
  { path: '/audit', title: '审计日志', icon: 'Document' },
  { path: '/settings', title: '系统设置', icon: 'Setting' },
]

/** Base document title, used for views that are not in the menu (login). */
export const appTitle = 'sspanel · Shadowsocks 控制台'

/**
 * matchNav resolves the menu entry a path belongs to.
 *
 * It matches the longest menu path that the given path starts with, so a nested
 * URL still reports its section. The root path only matches itself, so
 * `/inbounds` is not swallowed by `/`. Paths outside the menu return undefined.
 */
export function matchNav(path: string): NavItem | undefined {
  let best: NavItem | undefined
  for (const item of navItems) {
    if (path !== item.path && !path.startsWith(`${item.path}/`)) continue
    if (!best || item.path.length > best.path.length) best = item
  }
  return best
}

/** titleOf is the page name for a path, or an empty string outside the menu. */
export function titleOf(path: string): string {
  return matchNav(path)?.title ?? ''
}
