import { describe, expect, it } from 'vitest'
import { absoluteURL, normalizeBasePath } from '../src/utils/subscription'

// The share dialog used to copy the raw `/sub/<token>` path the server returns.
// That is only a valid URL when the panel is mounted at the site root, so a
// sub-path deployment handed out a broken address. These cases pin the join.

describe('normalizeBasePath', () => {
  it('treats the root and empty paths as no prefix', () => {
    expect(normalizeBasePath('')).toBe('')
    expect(normalizeBasePath('/')).toBe('')
    expect(normalizeBasePath('   ')).toBe('')
  })

  it('keeps a sub path and drops the trailing slash', () => {
    expect(normalizeBasePath('/panel')).toBe('/panel')
    expect(normalizeBasePath('/panel/')).toBe('/panel')
    expect(normalizeBasePath('panel')).toBe('/panel')
    expect(normalizeBasePath('panel/')).toBe('/panel')
  })
})

describe('absoluteURL', () => {
  it('joins the origin and path at the site root', () => {
    expect(absoluteURL('', '/sub/abc', 'https://node.example.com')).toBe(
      'https://node.example.com/sub/abc',
    )
    expect(absoluteURL('/', '/sub/abc', 'https://node.example.com')).toBe(
      'https://node.example.com/sub/abc',
    )
  })

  it('prefixes a sub-path deployment', () => {
    // The bug: the copied URL lacked /panel and 404'd on this deployment.
    expect(absoluteURL('/panel', '/sub/abc', 'https://node.example.com')).toBe(
      'https://node.example.com/panel/sub/abc',
    )
    expect(absoluteURL('/panel/', '/sub/abc', 'https://node.example.com')).toBe(
      'https://node.example.com/panel/sub/abc',
    )
    expect(absoluteURL('panel/', '/sub/abc', 'https://node.example.com')).toBe(
      'https://node.example.com/panel/sub/abc',
    )
  })

  it('keeps the origin port', () => {
    expect(absoluteURL('', '/sub/abc', 'https://node.example.com:8443')).toBe(
      'https://node.example.com:8443/sub/abc',
    )
    expect(absoluteURL('/panel', '/sub/abc', 'http://127.0.0.1:2096')).toBe(
      'http://127.0.0.1:2096/panel/sub/abc',
    )
  })

  it('tolerates a path without a leading slash and a trailing origin slash', () => {
    expect(absoluteURL('', 'sub/abc', 'https://node.example.com/')).toBe(
      'https://node.example.com/sub/abc',
    )
    expect(absoluteURL('/panel', 'sub/abc', 'https://node.example.com/')).toBe(
      'https://node.example.com/panel/sub/abc',
    )
  })

  it('returns an already absolute path unchanged', () => {
    expect(absoluteURL('/panel', 'https://cdn.example.com/sub/abc', 'https://node.example.com')).toBe(
      'https://cdn.example.com/sub/abc',
    )
  })

  it('returns an empty string for an empty path', () => {
    expect(absoluteURL('/panel', '', 'https://node.example.com')).toBe('')
  })
})
