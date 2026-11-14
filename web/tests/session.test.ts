import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath, URL } from 'node:url'
import { describe, expect, it } from 'vitest'
import { ApiError } from '../src/api'

// Guards for behaviours that live inside components/views.
//
// The project has no jsdom and no @vue/test-utils, so the flow itself cannot be
// mounted here. Instead these tests read the sources and assert the invariants a
// future refactor is most likely to drop. They are intentionally narrow: each
// one names the bug it prevents.

const srcDir = fileURLToPath(new URL('../src', import.meta.url))

function read(relativePath: string): string {
  return readFileSync(join(srcDir, relativePath), 'utf8')
}

describe('api session handlers', () => {
  it('keeps the redirect registration in api.ts instead of importing the router', () => {
    // api.ts must not import the router: router.ts imports basePath from api.ts,
    // and the cycle would leave one of them half initialised.
    const text = read('api.ts')
    expect(text).toMatch(/export function setUnauthorizedHandler/)
    expect(text).toMatch(/export function setPasswordRequiredHandler/)
    expect(text).not.toMatch(/from ['"]\.\/router['"]|from ['"]@\/router['"]/)
  })

  it('dispatches 401 and password_change_required to the handlers', () => {
    const text = read('api.ts')
    expect(text).toMatch(/apiError\.status === 401/)
    expect(text).toMatch(/unauthorizedHandler\?\.\(\)/)
    expect(text).toMatch(/apiError\.code === 'password_change_required'/)
    expect(text).toMatch(/passwordRequiredHandler\?\.\(\)/)
  })

  it('does not treat a failed login as an expired session', () => {
    // POST /auth/login answers 401 for wrong credentials; redirecting to /login
    // there would swallow the error message the login view needs to show.
    const text = read('api.ts')
    expect(text).toMatch(/isLoginRequest/)
    expect(text).toMatch(/!isLoginRequest\(path\)/)
  })

  it('registers both handlers in main.ts and dedupes the redirects', () => {
    const text = read('main.ts')
    expect(text).toMatch(/setUnauthorizedHandler\(/)
    expect(text).toMatch(/setPasswordRequiredHandler\(/)
    expect(text).toMatch(/redirectingToLogin/)
    expect(text).toMatch(/redirectingToPassword/)
    // A redirect to the page already shown must not happen again.
    expect(text).toMatch(/name: 'settings', query: \{ force: 'password' \}/)
  })

  it('exposes the API error status so fetchMe can tell 401 from a network error', () => {
    const error = new ApiError(401, 'unauthorized', 'login required')
    expect(error).toBeInstanceOf(Error)
    expect(error.status).toBe(401)
    expect(new ApiError(500, 'internal_error', 'boom').status).toBe(500)
  })
})

describe('auth store session handling', () => {
  it('only clears the admin on an explicit 401', () => {
    // A network blip used to log the operator out; fetchMe now inspects the
    // status and keeps the cached admin for anything else.
    const text = read('stores/auth.ts')
    expect(text).toMatch(/error instanceof ApiError && error\.status === 401/)
    expect(text).toMatch(/this\.admin = null/)
    expect(text).toMatch(/this\.checked = true/)
  })
})

describe('forced password change flow', () => {
  it('does not load settings while a password change is pending', () => {
    const text = read('views/SettingsView.vue')
    expect(text).toMatch(/if \(forced\.value\) return/)
    expect(text).toMatch(/passwordVisible\.value = true/)
  })

  it('disables saving until the settings were actually loaded', () => {
    const text = read('views/SettingsView.vue')
    expect(text).toMatch(/const loaded = ref\(false\)/)
    expect(text).toMatch(/:disabled="!loaded"/)
    expect(text).toMatch(/if \(!loaded\.value \|\| forced\.value\) return/)
  })

  it('clears the force query and reloads after the password was saved', () => {
    const text = read('views/SettingsView.vue')
    expect(text).toMatch(/@saved="onPasswordSaved"/)
    expect(text).toMatch(/router\.replace\(\{ query: \{\} \}\)/)
    expect(text).toMatch(/async function onPasswordSaved/)
  })

  it('emits saved from the password dialog', () => {
    const text = read('components/PasswordDialog.vue')
    expect(text).toMatch(/\(e: 'saved'\): void/)
    expect(text).toMatch(/emit\('saved'\)/)
  })
})

describe('subscription dialog', () => {
  it('copies the absolute subscription URL, not the raw server path', () => {
    const text = read('components/ClientLinkDialog.vue')
    expect(text).toMatch(/absoluteURL\(basePath, subURL\.value/)
    expect(text).toMatch(/copy\(absoluteSub\)/)
    expect(text).not.toMatch(/copy\(subURL\)/)
  })

  it('offers a reset entry that re-renders the QR code', () => {
    const text = read('components/ClientLinkDialog.vue')
    expect(text).toMatch(/api\.resetClientToken\(/)
    expect(text).toMatch(/ElMessageBox\.confirm/)
    // Reloading the link re-renders the QR image from the new token.
    expect(text).toMatch(/await load\(\)/)
    expect(text).toMatch(/重置订阅/)
  })

  it('invalidates a late link response from the previous client', () => {
    const text = read('components/ClientLinkDialog.vue')
    expect(text).toMatch(/loadSeq/)
    expect(text).toMatch(/seq !== loadSeq \|\| clientId !== props\.clientId/)
  })
})

describe('request race guards', () => {
  it('guards the usage dialog against a slower earlier window', () => {
    const text = read('components/ClientUsageDialog.vue')
    expect(text).toMatch(/let loadSeq = 0/)
    expect(text).toMatch(/seq !== loadSeq/)
    expect(text).toMatch(/requestedDays !== days\.value/)
  })

  it('guards the audit page against a slower earlier page', () => {
    const text = read('views/AuditView.vue')
    expect(text).toMatch(/let loadSeq = 0/)
    expect(text).toMatch(/const seq = \+\+loadSeq/)
    expect(text).toMatch(/if \(seq !== loadSeq\) return/)
  })

  it('guards the inbound page against a slower earlier refresh', () => {
    const text = read('views/InboundsView.vue')
    expect(text).toMatch(/let loadSeq = 0/)
    expect(text).toMatch(/const seq = \+\+loadSeq/)
    expect(text).toMatch(/if \(seq !== loadSeq\) return/)
  })

  it('clamps the chart hover index when the series changes', () => {
    const text = read('components/TrafficChart.vue')
    expect(text).toMatch(/clampActive\(active\.value, props\.labels\.length\)/)
    // The marker must not dereference a point that no longer exists.
    expect(text).not.toMatch(/entry\.points\[active\]\.y/)
    expect(text).toMatch(/entry\.points\[index\] !== undefined/)
  })
})

describe('list refresh error handling', () => {
  it('reports a failed inbound refresh instead of rejecting', () => {
    const text = read('views/ClientsView.vue')
    // loadInbounds is caught at its own boundary, and reload joins the two legs
    // without ever letting a rejection escape to the fire-and-forget callers.
    expect(text).toMatch(/async function loadInbounds\(\): Promise<boolean>/)
    expect(text).toMatch(/async function reload\(\): Promise<boolean>/)
    expect(text).toMatch(/await Promise\.all\(\[loadInbounds\(\), load\(\)\]\)/)
    expect(text).toMatch(/加载入站列表失败/)
  })

  it('uses reload() for the refresh button', () => {
    const text = read('views/ClientsView.vue')
    expect(text).toMatch(/<el-button :icon="Refresh" :loading="loading" @click="reload">刷新<\/el-button>/)
  })

  it('keeps the delete result separate from the refresh result', () => {
    for (const view of ['views/ClientsView.vue', 'views/InboundsView.vue']) {
      const text = read(view)
      // The delete must be its own try block: a refresh failure inside it used to
      // report "删除失败" for a delete that had already succeeded.
      expect(text, view).toMatch(/已删除，但刷新列表失败/)
      expect(text, view).toMatch(/catch \(error: any\) \{\s*\n\s*ElMessage\.error\(error\?\.message \|\| '删除失败'\)/)
    }
  })

  it('refetches the filtered page after a traffic reset', () => {
    const text = read('views/ClientsView.vue')
    expect(text).toMatch(/if \(statusFilter\.value === 'all'\)/)
    expect(text).toMatch(/await load\(\)/)
  })

  it('guards the import dialog against a stale default inbound', () => {
    const text = read('components/ClientImportDialog.vue')
    expect(text).toMatch(/resolveImportInbound\(props\.defaultInboundId, props\.inbounds\)/)
    expect(text).toMatch(/watch\(\s*\n?\s*\(\) => props\.modelValue/)
  })
})
