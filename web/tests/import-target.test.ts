import { describe, expect, it } from 'vitest'
import { resolveImportInbound } from '../src/utils/import-target'

// The import dialog captured the target inbound once, at setup. Opening it again
// after switching the list filter preselected the previous inbound, which sent a
// batch to the wrong one. The rule is extracted here so it is testable without
// mounting the component.

const INBOUNDS = [{ id: 3 }, { id: 7 }, { id: 9 }]

describe('resolveImportInbound', () => {
  it('uses the inbound the list currently points at', () => {
    expect(resolveImportInbound(7, INBOUNDS)).toBe(7)
    expect(resolveImportInbound(9, INBOUNDS)).toBe(9)
  })

  it('falls back to the first inbound when the default is 0', () => {
    // "All inbounds" filter: the operator still has to pick something.
    expect(resolveImportInbound(0, INBOUNDS)).toBe(3)
  })

  it('falls back to the first inbound when the default no longer exists', () => {
    // The selected inbound was deleted or filtered away after the dialog opened.
    expect(resolveImportInbound(42, INBOUNDS)).toBe(3)
  })

  it('returns 0 when there is nothing to import into', () => {
    expect(resolveImportInbound(0, [])).toBe(0)
  })
})
