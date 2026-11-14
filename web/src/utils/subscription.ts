// Subscription URL helpers.
//
// The server returns subscription_url as a root-relative path (/sub/<token>) in
// the panel's own coordinate space: it does not know the deployment base path
// and it does not know the origin the browser reached it on. Both are frontend
// concerns, so the dialog composes the absolute URL here instead of copying the
// raw path (which handed the user a URL that only worked at the site root).
//
// The module has no imports so it stays unit testable without a DOM.

/**
 * normalizeBasePath turns the runtime base path into a prefix without a
 * trailing slash. Empty, "/" and "panel" all mean "mounted at the site root".
 */
export function normalizeBasePath(basePath: string): string {
  const trimmed = (basePath || '').trim()
  if (!trimmed || trimmed === '/') return ''
  return `/${trimmed.replace(/^\/+/, '').replace(/\/+$/, '')}`
}

/**
 * absoluteURL joins an origin, the deployment base path and a server-relative
 * path into one absolute URL.
 *
 *   absoluteURL('', '/sub/abc', 'https://node.example.com:8443')
 *     -> 'https://node.example.com:8443/sub/abc'
 *   absoluteURL('/panel/', '/sub/abc', 'https://node.example.com')
 *     -> 'https://node.example.com/panel/sub/abc'
 *
 * A path that is already absolute is returned untouched: tokens and hosts are
 * server controlled, so this never has to rewrite a URL.
 */
export function absoluteURL(basePath: string, path: string, origin: string): string {
  if (!path) return ''
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(path)) return path
  const base = normalizeBasePath(basePath)
  const suffix = path.startsWith('/') ? path : `/${path}`
  const root = (origin || '').replace(/\/+$/, '')
  return `${root}${base}${suffix}`
}
