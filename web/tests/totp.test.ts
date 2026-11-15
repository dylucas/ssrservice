import { describe, expect, it } from 'vitest'
import {
  formatRecoveryCode,
  formatRecoveryCodes,
  looksLikeTOTPCode,
  normalizeTOTPInput,
} from '../src/utils/totp'

// The second login step takes either an authenticator code or a recovery code.
// These helpers are what make "paste it however you copied it" work, so the
// boundaries (empty, hyphenated, lowercase, wrong length) are the point.

describe('normalizeTOTPInput', () => {
  it('trims and removes spaces and hyphens', () => {
    expect(normalizeTOTPInput('  123456  ')).toBe('123456')
    expect(normalizeTOTPInput('123 456')).toBe('123456')
    expect(normalizeTOTPInput('abcd-efgh-ijkl')).toBe('ABCDEFGHIJKL')
    expect(normalizeTOTPInput('abcd efgh ijkl')).toBe('ABCDEFGHIJKL')
  })

  it('uppercases recovery codes', () => {
    expect(normalizeTOTPInput('mnopqrstuvwx')).toBe('MNOPQRSTUVWX')
  })

  it('keeps an empty input empty', () => {
    expect(normalizeTOTPInput('')).toBe('')
    expect(normalizeTOTPInput('   ')).toBe('')
    expect(normalizeTOTPInput('---')).toBe('')
  })
})

describe('looksLikeTOTPCode', () => {
  it('accepts exactly six digits, formatted or not', () => {
    expect(looksLikeTOTPCode('123456')).toBe(true)
    expect(looksLikeTOTPCode(' 123456 ')).toBe(true)
    expect(looksLikeTOTPCode('123 456')).toBe(true)
    expect(looksLikeTOTPCode('123-456')).toBe(true)
  })

  it('rejects everything else', () => {
    expect(looksLikeTOTPCode('')).toBe(false)
    expect(looksLikeTOTPCode('12345')).toBe(false)
    expect(looksLikeTOTPCode('1234567')).toBe(false)
    expect(looksLikeTOTPCode('ABCDEF')).toBe(false)
    expect(looksLikeTOTPCode('12a456')).toBe(false)
  })
})

describe('formatRecoveryCode', () => {
  it('groups a 12 character code into fours', () => {
    expect(formatRecoveryCode('ABCDEFGHIJKL')).toBe('ABCD-EFGH-IJKL')
    expect(formatRecoveryCode('abcdefghijkl')).toBe('ABCD-EFGH-IJKL')
  })

  it('accepts already hyphenated or spaced codes', () => {
    expect(formatRecoveryCode('ABCD-EFGH-IJKL')).toBe('ABCD-EFGH-IJKL')
    expect(formatRecoveryCode('abcd efgh ijkl')).toBe('ABCD-EFGH-IJKL')
  })

  it('returns anything that is not 12 characters unchanged', () => {
    expect(formatRecoveryCode('SHORT')).toBe('SHORT')
    expect(formatRecoveryCode('ABCDEFGHIJKLM')).toBe('ABCDEFGHIJKLM')
    expect(formatRecoveryCode('')).toBe('')
  })
})

describe('formatRecoveryCodes', () => {
  it('renders one formatted code per line', () => {
    expect(formatRecoveryCodes(['ABCDEFGHIJKL', 'mnopqrstuvwx'])).toBe(
      'ABCD-EFGH-IJKL\nMNOP-QRST-UVWX',
    )
  })

  it('returns an empty string for an empty list', () => {
    expect(formatRecoveryCodes([])).toBe('')
  })
})
