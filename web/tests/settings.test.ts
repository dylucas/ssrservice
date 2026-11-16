import { describe, expect, it } from 'vitest'
import {
  SETTINGS_FIELDS,
  describeEndpoint,
  endpointSource,
  isSettingsDirty,
} from '../src/utils/settings'
import type { Settings } from '../src/types'

// The settings page has two rules that are easy to get subtly wrong: the fallback
// order for the address a share link uses (the server implements it, so the UI
// must describe it rather than invent one) and when the save button is offered.
// Both live in utils/settings.ts so they can be checked here.

function settings(overrides: Partial<Settings> = {}): Settings {
  return {
    public_host: 'node.example.com',
    public_port: 443,
    subscription_enabled: true,
    allow_private_targets: false,
    reset_day: 1,
    timezone: 'Asia/Shanghai',
    access_log_enabled: false,
    ...overrides,
  }
}

describe('isSettingsDirty', () => {
  it('is false for an untouched form', () => {
    expect(isSettingsDirty(settings(), settings())).toBe(false)
  })

  it('detects a change in each field', () => {
    const changes: Partial<Settings>[] = [
      { public_host: 'other.example.com' },
      { public_port: 8443 },
      { subscription_enabled: false },
      { allow_private_targets: true },
      { reset_day: 15 },
      { timezone: 'UTC' },
      { access_log_enabled: true },
    ]
    for (const change of changes) {
      const form = { ...settings(), ...change }
      expect(isSettingsDirty(form, settings()), JSON.stringify(change)).toBe(true)
    }
  })

  it('covers every field the form edits', () => {
    // A setting added to the type but forgotten here would never enable the save
    // button, which looks exactly like "nothing to save".
    expect([...SETTINGS_FIELDS].sort()).toEqual(
      Object.keys(settings()).sort(),
    )
  })

  it('ignores surrounding whitespace in the host', () => {
    expect(isSettingsDirty(settings({ public_host: '  node.example.com ' }), settings())).toBe(false)
    expect(isSettingsDirty(settings({ public_host: '' }), settings({ public_host: '' }))).toBe(false)
    expect(isSettingsDirty(settings({ public_host: '  ' }), settings({ public_host: '' }))).toBe(false)
  })

  it('compares ports as numbers', () => {
    const saved = settings({ public_port: 0 })
    expect(isSettingsDirty({ ...saved, public_port: 0 }, saved)).toBe(false)
    expect(isSettingsDirty({ ...saved, public_port: 8388 }, saved)).toBe(true)
  })
})

describe('describeEndpoint', () => {
  it('uses the global host and port when both are set', () => {
    expect(describeEndpoint('node.example.com', 443, 8388)).toBe('node.example.com:443')
  })

  it('falls back to the inbound port when the global port is 0', () => {
    expect(describeEndpoint('node.example.com', 0, 8388)).toBe('node.example.com:8388')
  })

  it('says so when there is no global host', () => {
    expect(describeEndpoint('', 0, 8388)).toBe('入站监听地址:8388')
    expect(describeEndpoint('   ', 443, 8388)).toBe('入站监听地址:443')
  })

  it('omits the port when nothing has one', () => {
    expect(describeEndpoint('node.example.com', 0, 0)).toBe('node.example.com')
    expect(describeEndpoint('', 0, 0)).toBe('入站监听地址')
  })
})

describe('endpointSource', () => {
  it('explains whether the port is explicit or a fallback', () => {
    expect(endpointSource(443)).toBe('使用上面填写的公共端口')
    expect(endpointSource(0)).toBe('留空时回退到各入站自己的端口')
  })
})
