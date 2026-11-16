import type { BatchAction, BatchRequest } from '@/types'

/**
 * buildBatchRequest renders the body of a batch operation.
 *
 * Only the fields the action actually uses are sent: sending `days` on a delete
 * or `bytes` on a reset invites confusion about which one the server honoured,
 * and the endpoint rejects a bad combination.
 */
export function buildBatchRequest(
  action: BatchAction,
  ids: number[],
  options: { days?: number; bytes?: number } = {},
): BatchRequest {
  const request: BatchRequest = { action, ids: [...ids] }
  if (action === 'extend') request.days = options.days ?? 30
  if (action === 'add_quota') request.bytes = options.bytes ?? 1024 * 1024 * 1024
  return request
}

/** MAX selection size the panel will send in one batch. */
export const BATCH_MAX = 500

/** batchNeedsConfirm reports which actions are destructive enough to confirm. */
export function batchNeedsConfirm(action: BatchAction): boolean {
  return action === 'delete' || action === 'reset'
}

/** batchLabel is the short action name used in the confirmation dialog. */
export function batchLabel(action: BatchAction): string {
  switch (action) {
    case 'enable': return '启用'
    case 'disable': return '禁用'
    case 'delete': return '删除'
    case 'extend': return '延长 30 天'
    case 'add_quota': return '增加 1 GB 流量'
    case 'reset': return '清零流量'
  }
}
