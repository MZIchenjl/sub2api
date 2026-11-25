import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { DevinTokenInfo } from '@/api/admin/devin'

export function useDevinOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const authUrl = ref('')
  const sessionId = ref('')
  const state = ref('')
  const loading = ref(false)
  const error = ref('')

  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    loading.value = false
    error.value = ''
  }

  const generateAuthUrl = async (proxyId: number | null | undefined): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    error.value = ''

    try {
      const payload: Record<string, unknown> = {}
      if (proxyId) payload.proxy_id = proxyId
      const response = await adminAPI.devin.generateAuthUrl(payload as any)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.failedToGenerateUrl')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const exchangeAuthCode = async (params: {
    code: string
    sessionId: string
    state?: string
    proxyId?: number | null
  }): Promise<DevinTokenInfo | null> => {
    const code = params.code?.trim()
    if (!code || !params.sessionId) {
      error.value = t('admin.accounts.oauth.missingExchangeParams')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId,
        code
      }
      if (params.state?.trim()) payload.state = params.state.trim()
      if (params.proxyId) payload.proxy_id = params.proxyId

      return await adminAPI.devin.exchangeCode(payload as any)
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.authFailed')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const validateToken = async (
    token: string,
    baseUrl?: string,
    proxyId?: number | null
  ): Promise<DevinTokenInfo | null> => {
    const trimmed = token.trim()
    if (!trimmed) {
      error.value = t('admin.accounts.oauth.devin.pleaseEnterToken')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = { token: trimmed }
      if (baseUrl?.trim()) payload.base_url = baseUrl.trim()
      if (proxyId) payload.proxy_id = proxyId
      return await adminAPI.devin.validateToken(payload as any)
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.devin.failedToValidateToken')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (
    tokenInfo: DevinTokenInfo,
    customBaseUrl?: string
  ): Record<string, unknown> => {
    const creds: Record<string, unknown> = {
      session_token: tokenInfo.session_token
    }
    const baseUrl = customBaseUrl?.trim() || tokenInfo.base_url?.trim()
    if (baseUrl) creds.base_url = baseUrl
    if (tokenInfo.email) creds.email = tokenInfo.email
    if (tokenInfo.user_name) creds.user_name = tokenInfo.user_name
    if (tokenInfo.user_id) creds.user_id = tokenInfo.user_id
    if (tokenInfo.org_id) creds.org_id = tokenInfo.org_id
    if (tokenInfo.org_name) creds.org_name = tokenInfo.org_name
    if (tokenInfo.team_id) creds.team_id = tokenInfo.team_id
    if (tokenInfo.plan) creds.plan = tokenInfo.plan
    return creds
  }

  const buildExtra = (tokenInfo: DevinTokenInfo): Record<string, unknown> => {
    const extra: Record<string, unknown> = {}
    if (tokenInfo.email) {
      extra.email = tokenInfo.email
      extra.devin_email = tokenInfo.email
    }
    if (tokenInfo.plan) extra.devin_plan = tokenInfo.plan
    if (tokenInfo.org_name) extra.devin_org_name = tokenInfo.org_name
    if (typeof tokenInfo.daily_quota_remaining_percent === 'number') {
      extra.devin_daily_quota_remaining_percent = tokenInfo.daily_quota_remaining_percent
    }
    if (typeof tokenInfo.weekly_quota_remaining_percent === 'number') {
      extra.devin_weekly_quota_remaining_percent = tokenInfo.weekly_quota_remaining_percent
    }
    if (tokenInfo.daily_quota_reset_at) extra.devin_daily_quota_reset_at = tokenInfo.daily_quota_reset_at
    if (tokenInfo.weekly_quota_reset_at) extra.devin_weekly_quota_reset_at = tokenInfo.weekly_quota_reset_at
    return extra
  }

  return {
    authUrl,
    sessionId,
    state,
    loading,
    error,
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    validateToken,
    buildCredentials,
    buildExtra
  }
}
