import { describe, expect, it } from 'vitest'
import {
  CONFIG_FILE_ACCEPT,
  IMPORT_CONFIRM_MESSAGE,
  configImportError,
  configImportSummary,
  parseConfigFile,
} from '../src/utils/config-migration'

describe('config migration helpers', () => {
  it('warns that an import replaces everything and invalidates sessions', () => {
    for (const word of ['覆盖', '失效', '不可撤销']) {
      expect(IMPORT_CONFIRM_MESSAGE).toContain(word)
    }
  })

  it('summarises a successful import and mentions warnings', () => {
    expect(configImportSummary({ inbounds: 2, clients: 5, reloads: 2 }))
      .toBe('已导入 2 个入站、5 个用户')
    expect(configImportSummary({ inbounds: 1, clients: 0, reloads: 0, warnings: ['port'] }))
      .toContain('1 个警告')
  })

  it('renders field errors before a generic message', () => {
    const withFields = {
      error: { message: '配置校验失败', fields: { 'inbounds[2].port': 'duplicate', 'settings.timezone': 'invalid' } },
    }
    expect(configImportError(withFields)).toEqual([
      'inbounds[2].port: duplicate',
      'settings.timezone: invalid',
    ])
    expect(configImportError({ message: 'network down' })).toEqual(['network down'])
    expect(configImportError(null)).toEqual(['导入失败'])
  })

  it('parses a config file and rejects malformed input', () => {
    const parsed = parseConfigFile('{"version":1,"inbounds":[]}')
    expect(parsed.version).toBe(1)
    expect(() => parseConfigFile('not json')).toThrow('不是有效的 JSON')
    expect(() => parseConfigFile('null')).toThrow('格式不正确')
    expect(CONFIG_FILE_ACCEPT).toContain('.json')
  })
})
