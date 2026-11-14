import { defineStore } from 'pinia'
import { ApiError, api } from '@/api'
import type { Admin } from '@/types'

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
    async login(username: string, password: string) {
      const result = await api.login(username, password)
      this.admin = result.admin
      this.checked = true
      return this.admin
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
