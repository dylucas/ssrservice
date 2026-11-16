import type { ConfigSnapshot } from '@/types'

/**
 * Pure helpers for the configuration-migration card in settings.
 *
 * The dangerous part of an import is not the HTTP request, it is the moment the
 * operator realises the file will replace everything. Keeping the confirmation
 * text and the error rendering here lets them be tested without a DOM.
 */

/** The warning shown before an import is sent. */
export const IMPORT_CONFIRM_MESSAGE =
  '导入会覆盖当前全部入站、用户、面板设置与管理员凭据，并让所有已登录会话失效。此操作不可撤销。'

/** configImportSummary renders a successful import result. */
export function configImportSummary(result: { inbounds: number; clients: number; reloads: number; warnings?: string[] }): string {
  const base = `已导入 ${result.inbounds} 个入站、${result.clients} 个用户`
  return result.warnings?.length ? `${base}（${result.warnings.length} 个警告，请检查日志）` : base
}

/**
 * configImportError pulls the field-level messages from the API error envelope
 * into a short list, so an operator sees "inbounds[2].port" instead of a wall of
 * text. A plain error message is kept when the server did not report fields.
 */
export function configImportError(error: unknown): string[] {
  const envelope = error as {
    message?: string
    error?: { message?: string; fields?: Record<string, string> }
  } | null
  const fields = envelope?.error?.fields
  if (fields && Object.keys(fields).length) {
    return Object.entries(fields).slice(0, 5).map(([field, message]) => `${field}: ${message}`)
  }
  return [envelope?.error?.message || envelope?.message || '导入失败']
}

/**
 * parseConfigFile parses an uploaded file into a snapshot, with a friendly
 * error when the JSON is malformed (a common mistake when editing by hand).
 */
export function parseConfigFile(text: string): ConfigSnapshot {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    throw new Error('文件不是有效的 JSON')
  }
  if (!parsed || typeof parsed !== 'object') {
    throw new Error('配置文件的格式不正确')
  }
  return parsed as ConfigSnapshot
}

/** configFileAccept is the accept filter of the file input. */
export const CONFIG_FILE_ACCEPT = 'application/json,.json'
