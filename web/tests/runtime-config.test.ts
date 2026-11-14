import { describe, expect, it } from 'vitest'
import { parseRuntimeConfig } from '../src/runtime-config'

describe('parseRuntimeConfig', () => {
  it('reads basePath and version', () => {
    const config = parseRuntimeConfig('{"basePath":"/panel/","version":"1.2.3"}')
    expect(config).toEqual({ basePath: '/panel/', version: '1.2.3' })
  })

  it('treats empty input as empty config', () => {
    expect(parseRuntimeConfig(null)).toEqual({})
    expect(parseRuntimeConfig(undefined)).toEqual({})
    expect(parseRuntimeConfig('')).toEqual({})
  })

  it('ignores malformed or non-object payloads', () => {
    expect(parseRuntimeConfig('not json')).toEqual({})
    expect(parseRuntimeConfig('null')).toEqual({})
    expect(parseRuntimeConfig('[]')).toEqual({})
    expect(parseRuntimeConfig('42')).toEqual({})
  })

  it('drops fields with unexpected types', () => {
    expect(parseRuntimeConfig('{"basePath":1,"version":null}')).toEqual({})
    expect(parseRuntimeConfig('{"basePath":"/","version":true}')).toEqual({ basePath: '/' })
  })
})
