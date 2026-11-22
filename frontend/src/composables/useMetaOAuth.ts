import { ref, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { MetaTokenInfo } from '@/api/admin/meta'

export function useMetaOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const sessionId = ref('')
  const userCode = ref('')
  const verificationUri = ref('')
  const verificationUriComplete = ref('')
  const expiresIn = ref(0)
  const pollInterval = ref(5)
  const polling = ref(false)
  const loading = ref(false)
  const error = ref('')

  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let pollGeneration = 0

  const stopPolling = () => {
    pollGeneration++
    if (pollTimer !== null) {
      clearTimeout(pollTimer)
      pollTimer = null
    }
    polling.value = false
  }

  const resetState = () => {
    stopPolling()
    sessionId.value = ''
    userCode.value = ''
    verificationUri.value = ''
    verificationUriComplete.value = ''
    expiresIn.value = 0
    pollInterval.value = 5
    loading.value = false
    error.value = ''
  }

  const startDeviceFlow = async (
    proxyId: number | null | undefined,
    onCompleted?: (token: MetaTokenInfo) => void
  ): Promise<boolean> => {
    resetState()
    loading.value = true
    try {
      const payload: Record<string, unknown> = {}
      if (proxyId) payload.proxy_id = proxyId
      const resp = await adminAPI.meta.startDeviceFlow(payload as any)
      sessionId.value = resp.session_id
      userCode.value = resp.user_code
      verificationUri.value = resp.verification_uri
      verificationUriComplete.value = resp.verification_uri_complete || resp.verification_uri
      expiresIn.value = resp.expires_in
      pollInterval.value = Math.max(2, resp.interval || 5)
      startPollingLoop(onCompleted)
      return true
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.failedToGenerateUrl')
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  const startPollingLoop = (onCompleted?: (token: MetaTokenInfo) => void) => {
    stopPolling()
    if (!sessionId.value) return
    const gen = ++pollGeneration
    polling.value = true

    const scheduleNext = (delaySec: number) => {
      pollTimer = setTimeout(async () => {
        if (gen !== pollGeneration || !sessionId.value) return
        try {
          const resp = await adminAPI.meta.pollDeviceFlow({ session_id: sessionId.value })
          if (gen !== pollGeneration) return
          if (resp.status === 'completed' && resp.token) {
            stopPolling()
            onCompleted?.(resp.token)
            return
          }
          if (resp.status === 'expired') {
            stopPolling()
            error.value = t('admin.accounts.oauth.deviceFlow.expired')
            return
          }
          if (resp.interval && resp.interval > 0) {
            pollInterval.value = resp.interval
          }
          scheduleNext(pollInterval.value)
        } catch (err: any) {
          if (gen !== pollGeneration) return
          stopPolling()
          error.value = err.response?.data?.detail || t('admin.accounts.oauth.authFailed')
          appStore.showError(error.value)
        }
      }, Math.max(2, delaySec) * 1000)
    }

    scheduleNext(pollInterval.value)
  }

  const validateToken = async (
    token: string,
    baseUrl?: string,
    proxyId?: number | null
  ): Promise<MetaTokenInfo | null> => {
    const trimmed = token.trim()
    if (!trimmed) {
      error.value = t('admin.accounts.oauth.meta.pleaseEnterToken')
      return null
    }
    loading.value = true
    error.value = ''
    try {
      const payload: Record<string, unknown> = { token: trimmed }
      if (baseUrl?.trim()) payload.base_url = baseUrl.trim()
      if (proxyId) payload.proxy_id = proxyId
      return await adminAPI.meta.validateToken(payload as any)
    } catch (err: any) {
      error.value = err.response?.data?.detail || t('admin.accounts.oauth.meta.failedToValidateToken')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  const buildCredentials = (
    tokenInfo: MetaTokenInfo,
    customBaseUrl?: string
  ): Record<string, unknown> => {
    const creds: Record<string, unknown> = {
      api_key: tokenInfo.api_key,
      base_url: customBaseUrl?.trim() || tokenInfo.base_url || 'https://api.meta.ai/v1'
    }
    if (tokenInfo.dca_token) creds.dca_token = tokenInfo.dca_token
    if (tokenInfo.token_type) creds.token_type = tokenInfo.token_type
    if (tokenInfo.scope) creds.scope = tokenInfo.scope
    if (tokenInfo.expires_at) creds.expires_at = String(tokenInfo.expires_at)
    if (tokenInfo.expires_in) creds.expires_in = String(tokenInfo.expires_in)
    return creds
  }

  const buildExtra = (tokenInfo: MetaTokenInfo): Record<string, unknown> => {
    const extra: Record<string, unknown> = {
      meta_auth_method: tokenInfo.dca_token ? 'oauth' : 'apikey'
    }
    return extra
  }

  onUnmounted(() => {
    stopPolling()
  })

  return {
    sessionId,
    userCode,
    verificationUri,
    verificationUriComplete,
    expiresIn,
    pollInterval,
    polling,
    loading,
    error,
    stopPolling,
    resetState,
    startDeviceFlow,
    validateToken,
    buildCredentials,
    buildExtra
  }
}
