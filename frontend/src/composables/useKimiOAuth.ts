import { ref, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { KimiOAuthDomain, KimiTokenInfo } from '@/api/admin/kimi'

export function useKimiOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  const domain = ref<KimiOAuthDomain>('kimi.com')
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
    onCompleted?: (token: KimiTokenInfo) => void
  ): Promise<boolean> => {
    resetState()
    loading.value = true
    try {
      const payload: Record<string, unknown> = { domain: domain.value }
      if (proxyId) payload.proxy_id = proxyId
      const resp = await adminAPI.kimi.startDeviceFlow(payload as any)
      sessionId.value = resp.session_id
      userCode.value = resp.user_code
      verificationUri.value = resp.verification_uri
      verificationUriComplete.value = resp.verification_uri_complete || resp.verification_uri
      expiresIn.value = resp.expires_in
      pollInterval.value = Math.max(2, resp.interval || 5)
      if (resp.domain) domain.value = resp.domain
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

  const startPollingLoop = (onCompleted?: (token: KimiTokenInfo) => void) => {
    stopPolling()
    if (!sessionId.value) return
    const gen = ++pollGeneration
    polling.value = true

    const scheduleNext = (delaySec: number) => {
      pollTimer = setTimeout(async () => {
        if (gen !== pollGeneration || !sessionId.value) return
        try {
          const resp = await adminAPI.kimi.pollDeviceFlow({ session_id: sessionId.value })
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

  const buildCredentials = (tokenInfo: KimiTokenInfo): Record<string, unknown> => {
    const apiBase = (tokenInfo.api_base_url || 'https://api.kimi.com/coding').replace(/\/+$/, '')
    const creds: Record<string, unknown> = {
      access_token: tokenInfo.access_token,
      refresh_token: tokenInfo.refresh_token,
      token_type: tokenInfo.token_type || 'Bearer',
      kimi_domain: tokenInfo.domain || domain.value,
      account_mode: 'coding',
      api_protocol: 'adaptive',
      base_url: `${apiBase}/v1`,
      api_base_urls: {
        chat_completions: `${apiBase}/v1`,
        anthropic: apiBase,
        responses: `${apiBase}/v1`
      }
    }
    if (tokenInfo.scope) creds.scope = tokenInfo.scope
    if (tokenInfo.device_id) creds.device_id = tokenInfo.device_id
    if (tokenInfo.expires_at) creds.expires_at = String(tokenInfo.expires_at)
    if (tokenInfo.expires_in) creds.expires_in = String(tokenInfo.expires_in)
    return creds
  }

  const buildExtra = (tokenInfo: KimiTokenInfo): Record<string, unknown> => {
    return {
      kimi_domain: tokenInfo.domain || domain.value,
      kimi_auth_method: 'oauth'
    }
  }

  onUnmounted(() => {
    stopPolling()
  })

  return {
    domain,
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
    buildCredentials,
    buildExtra
  }
}
