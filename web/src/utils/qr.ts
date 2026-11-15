// QR encoding shared by the client share-link dialog and the TOTP setup dialog.
//
// The encoder is ~26 kB that only those two dialogs need, so it is loaded on
// demand instead of riding along in any route chunk. web/tests/bundle.test.ts
// guards the dynamic import.

type QREncoder = (text: string, options: { margin: number; width: number }) => Promise<string>

let loadQRCode: (() => Promise<typeof import('qrcode')>) | null = null

/**
 * resolveEncoder picks toDataURL out of the module namespace.
 *
 * qrcode is CommonJS (`module.exports = require('./server')`), and a dynamic
 * import of it can surface the function either as a named export or under
 * `default` depending on the interop pass, so both shapes are accepted.
 */
function resolveEncoder(mod: typeof import('qrcode')): QREncoder | null {
  const named = (mod as { toDataURL?: QREncoder }).toDataURL
  if (named) return named
  const interop = (mod as { default?: { toDataURL?: QREncoder } }).default
  return interop?.toDataURL ?? null
}

/**
 * encodeQR renders value as a QR code data URL.
 *
 * A failure returns an empty string: the text content (share link, otpauth URL)
 * is what matters, and a missing QR image must not turn a whole dialog into an
 * error.
 */
export async function encodeQR(value: string, width = 220): Promise<string> {
  try {
    if (!loadQRCode) loadQRCode = () => import('qrcode')
    const encode = resolveEncoder(await loadQRCode())
    if (!encode) throw new Error('qrcode encoder unavailable')
    return await encode(value, { margin: 1, width })
  } catch {
    return ''
  }
}
