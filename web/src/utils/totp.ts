// Helpers for the TOTP inputs. The second login step accepts both a 6 digit
// authenticator code and a recovery code, and recovery codes are shown in
// XXXX-XXXX-XXXX groups. Normalising in one place keeps the login view, the
// setup dialog and the settings page from each accepting a slightly different
// spelling. This module is pure (no imports) so it is unit tested directly.

/**
 * normalizeTOTPInput strips the formatting an operator may paste: surrounding
 * whitespace, spaces and hyphens between groups. The server accepts any case, so
 * the value is uppercased here as well.
 */
export function normalizeTOTPInput(input: string): string {
  return input.trim().replace(/[\s-]+/g, '').toUpperCase()
}

/** looksLikeTOTPCode reports whether input is exactly six digits after normalising. */
export function looksLikeTOTPCode(input: string): boolean {
  return /^\d{6}$/.test(normalizeTOTPInput(input))
}

/**
 * formatRecoveryCode renders a 12 character recovery code as XXXX-XXXX-XXXX.
 * Values that are not 12 characters long are returned as they came in: a short
 * or malformed code should look wrong on screen, not be reshaped into a fake
 * one.
 */
export function formatRecoveryCode(code: string): string {
  const normalized = normalizeTOTPInput(code)
  if (normalized.length !== 12) return code
  return `${normalized.slice(0, 4)}-${normalized.slice(4, 8)}-${normalized.slice(8, 12)}`
}

/** formatRecoveryCodes renders a list one code per line for copy/download. */
export function formatRecoveryCodes(codes: string[]): string {
  return codes.map(formatRecoveryCode).join('\n')
}
