import { defineStore } from 'pinia'
import { ApiError, api } from '@/api'
import type { Admin, LoginResult, TOTPChallengeResponse } from '@/types'

/** isTOTPChallenge narrows the two shapes POST /auth/login can answer with. */
export function isTOTPChallenge(result: LoginResult): result is TOTPChallengeResponse {
  return result.totp_required === true
}

export const useAuthStore = defineStore('auth', {
  state: () => ({
    admin: null as Admin | null,
    checked: false,
  }),
  actions: {
    async fetchMe() {
      try {
        this.admin = await api.me()
      } catch (error) {
        // Only an explicit 401 means the session is gone. A network blip or a
        // 5xx must keep the cached admin: logging the operator out on a
        // transient error would hide a working session behind the login page.
        if (error instanceof ApiError && error.status === 401) {
          this.admin = null
        }
      } finally {
        this.checked = true
      }
    },
    /** clear drops the cached admin after a 401 so the next guard run redirects. */
    clear() {
      this.admin = null
      this.checked = true
    },
    /**
     * login runs the password step. When the account has 2FA enabled the server
     * answers with a challenge instead of a session, so no admin may be written
     * here: the caller has to finish the flow through loginTOTP first.
     */
    async login(username: string, password: string): Promise<LoginResult> {
      const result = await api.login(username, password)
      if (isTOTPChallenge(result)) return result
      this.admin = result.admin
      this.checked = true
      return result
    },
    /** loginTOTP completes a challenge and writes the session's admin. */
    async loginTOTP(challenge: string, code: string): Promise<Admin> {
      const result = await api.loginTOTP(challenge, code)
      this.admin = result.admin
      this.checked = true
      return result.admin
    },
    /** setTOTPEnabled mirrors a setup/disable result without a full re-fetch. */
    setTOTPEnabled(enabled: boolean) {
      if (this.admin) {
        this.admin = { ...this.admin, totp_enabled: enabled }
      }
    },
    async logout() {
      try {
        await api.logout()
      } finally {
        this.admin = null
        this.checked = true
      }
    },
    async changePassword(current: string, next: string) {
      await api.changePassword(current, next)
      if (this.admin) {
        this.admin = { ...this.admin, must_change_password: false }
      }
    },
  },
})
