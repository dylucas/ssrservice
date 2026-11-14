// Target-inbound resolution for the bulk import dialog.
//
// The dialog is opened from the client list, which already knows which inbound
// the operator is looking at. That default must be re-read every time the
// dialog opens: keeping the value captured at setup() preselects the inbound
// from the previous import, so a batch can be written into the wrong inbound.
//
// No imports of runtime code, so the rule is unit testable without a DOM.

interface InboundRef {
  id: number
}

/**
 * resolveImportInbound picks the inbound the import form should preselect.
 *
 * A positive default that still exists wins; otherwise the first inbound is the
 * only sensible fallback (0 means "nothing to import into", which disables the
 * submit button).
 */
export function resolveImportInbound(defaultId: number, inbounds: InboundRef[]): number {
  if (defaultId > 0 && inbounds.some((item) => item.id === defaultId)) {
    return defaultId
  }
  return inbounds[0]?.id ?? 0
}
