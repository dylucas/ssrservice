export interface RuntimeConfig {
  basePath?: string
  version?: string
}

/** DOM id of the JSON data block injected into index.html by the server. */
export const RUNTIME_CONFIG_ELEMENT_ID = 'sspanel-config'

/**
 * Parses the runtime configuration. Any malformed or non-object payload is
 * treated as empty so a corrupted data block cannot break the panel.
 */
export function parseRuntimeConfig(raw: string | null | undefined): RuntimeConfig {
  if (!raw) return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const record = parsed as Record<string, unknown>
    const config: RuntimeConfig = {}
    if (typeof record.basePath === 'string') config.basePath = record.basePath
    if (typeof record.version === 'string') config.version = record.version
    return config
  } catch {
    return {}
  }
}

/**
 * Reads the runtime configuration from the JSON data block, falling back to
 * the legacy `window.__SSPANEL__` global for older cached HTML.
 */
export function readRuntimeConfig(): RuntimeConfig {
  const element =
    typeof document === 'undefined' ? null : document.getElementById(RUNTIME_CONFIG_ELEMENT_ID)
  const fromBlock = parseRuntimeConfig(element?.textContent ?? null)
  const legacy = typeof window === 'undefined' ? undefined : window.__SSPANEL__
  return {
    basePath: fromBlock.basePath ?? legacy?.basePath,
    version: fromBlock.version ?? legacy?.version,
  }
}
