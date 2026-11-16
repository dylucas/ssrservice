import type { Settings } from '@/types'

// Helpers for the settings page.
//
// The page shows two things that are easy to get wrong in a template and worth
// testing on their own: what the address in a share link will actually resolve
// to, and whether the form has unsaved changes. Both are pure functions here
// rather than inline expressions, because the fallback order is a rule the
// server implements (inbound domain -> global host -> listen address, and
// domain port -> global port -> inbound port) and the UI must describe it
// without re-inventing it.

/** The fields the page can change. Kept explicit so a new setting cannot be
 * silently left out of the dirty check. */
export const SETTINGS_FIELDS = [
  'public_host',
  'public_port',
  'subscription_enabled',
  'allow_private_targets',
  'reset_day',
  'timezone',
  'access_log_enabled',
] as const satisfies readonly (keyof Settings)[]

/**
 * isSettingsDirty compares the form with the values last loaded from the server.
 *
 * The host is trimmed before comparing: a trailing space is a typo, not a
 * change, and treating it as one would leave the save button enabled forever.
 * Numbers are compared as numbers so 443 and "443" agree.
 */
export function isSettingsDirty(form: Settings, saved: Settings): boolean {
  return SETTINGS_FIELDS.some((field) => {
    if (field === 'public_host') {
      return (form[field] || '').trim() !== (saved[field] || '').trim()
    }
    if (typeof form[field] === 'number' || typeof saved[field] === 'number') {
      return Number(form[field] ?? 0) !== Number(saved[field] ?? 0)
    }
    return form[field] !== saved[field]
  })
}

/**
 * describeEndpoint renders the host:port a share link will use, given the two
 * global values and the port an inbound listens on.
 *
 *   describeEndpoint('node.example.com', 443, 8388) -> 'node.example.com:443'
 *   describeEndpoint('', 0, 8388)                   -> '入站监听地址:8388'
 *   describeEndpoint('node.example.com', 0, 8388)    -> 'node.example.com:8388'
 *
 * An inbound may override both, so the caller presents this as the default.
 */
export function describeEndpoint(
  publicHost: string,
  publicPort: number,
  inboundPort: number,
): string {
  const host = (publicHost || '').trim() || '入站监听地址'
  const port = Number(publicPort) > 0 ? Number(publicPort) : Number(inboundPort) > 0 ? Number(inboundPort) : 0
  return port > 0 ? `${host}:${port}` : host
}

/**
 * endpointSource explains where the port in describeEndpoint came from, so the
 * operator does not have to guess whether 8388 was their choice or a fallback.
 */
export function endpointSource(publicPort: number): string {
  return Number(publicPort) > 0 ? '使用上面填写的公共端口' : '留空时回退到各入站自己的端口'
}
