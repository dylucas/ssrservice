import { defineStore } from 'pinia'
import { api } from '@/api'
import type { Admin } from '@/types'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    admin: null as Admin | null,
    checked: false,
  }),
  actions: {
    async fetchMe() {
      try {
        this.admin = (await api.me()) as Admin
      } catch {
        this.admin = null
      } finally {
        this.checked = true
      }
    },
    async login(username: string, password: string) {
      const result = await api.login(username, password)
      this.admin = result.admin as Admin
      this.checked = true
      return this.admin
    },
    async logout() {
      try {
        await api.logout()
      } finally {
        this.admin = null
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
