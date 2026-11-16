import { describe, expect, it } from 'vitest'
import { BATCH_MAX, batchLabel, batchNeedsConfirm, buildBatchRequest } from '../src/utils/batch'

describe('buildBatchRequest', () => {
  it('sends only the fields the action uses', () => {
    expect(buildBatchRequest('enable', [1, 2])).toEqual({ action: 'enable', ids: [1, 2] })
    expect(buildBatchRequest('disable', [1, 2])).toEqual({ action: 'disable', ids: [1, 2] })
    expect(buildBatchRequest('delete', [1])).toEqual({ action: 'delete', ids: [1] })
    expect(buildBatchRequest('reset', [1])).toEqual({ action: 'reset', ids: [1] })
  })

  it('attaches defaults for extend and add_quota', () => {
    expect(buildBatchRequest('extend', [7])).toEqual({
      action: 'extend', ids: [7], days: 30,
    })
    expect(buildBatchRequest('add_quota', [7])).toEqual({
      action: 'add_quota', ids: [7], bytes: 1024 * 1024 * 1024,
    })
  })

  it('accepts an explicit amount and copies the ids', () => {
    const ids = [3, 1]
    const request = buildBatchRequest('extend', ids, { days: 90 })
    expect(request.days).toBe(90)
    expect(request.ids).not.toBe(ids) // a copy, so later edits cannot change the request
    request.ids.push(9)
    expect(ids).toEqual([3, 1])
  })

  it('never sends both days and bytes', () => {
    const both = buildBatchRequest('extend', [1], { days: 30, bytes: 999 })
    expect('bytes' in both).toBe(false)
    const quota = buildBatchRequest('add_quota', [1], { days: 30, bytes: 999 })
    expect('days' in quota).toBe(false)
  })
})

describe('batch confirmations', () => {
  it('only confirms the destructive actions', () => {
    expect(batchNeedsConfirm('delete')).toBe(true)
    expect(batchNeedsConfirm('reset')).toBe(true)
    for (const action of ['enable', 'disable', 'extend', 'add_quota'] as const) {
      expect(batchNeedsConfirm(action), action).toBe(false)
    }
  })

  it('labels every action', () => {
    for (const action of ['enable', 'disable', 'delete', 'extend', 'add_quota', 'reset'] as const) {
      expect(batchLabel(action).length).toBeGreaterThan(0)
    }
    expect(BATCH_MAX).toBe(500)
  })
})
