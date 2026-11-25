<template>
  <div class="space-y-4">
    <!-- Mode Tabs for Meta (Device Code OAuth vs Direct DCA Token / API Key) -->
    <div
      v-if="platform === 'meta'"
      class="flex rounded-lg bg-gray-100 p-1 dark:bg-dark-700"
      data-testid="meta-oauth-mode-tabs"
    >
      <button
        type="button"
        @click="metaMode = 'device'"
        :class="[
          'flex-1 rounded-md px-3 py-2 text-sm font-medium transition-all',
          metaMode === 'device'
            ? 'bg-white text-blue-600 shadow-sm dark:bg-dark-600 dark:text-blue-400'
            : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
        ]"
      >
        {{ t('admin.accounts.oauth.deviceFlow.deviceTab') }}
      </button>
      <button
        type="button"
        @click="metaMode = 'token'"
        :class="[
          'flex-1 rounded-md px-3 py-2 text-sm font-medium transition-all',
          metaMode === 'token'
            ? 'bg-white text-blue-600 shadow-sm dark:bg-dark-600 dark:text-blue-400'
            : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'
        ]"
      >
        {{ t('admin.accounts.oauth.meta.directTokenTab') }}
      </button>
    </div>

    <!-- Device Code Flow Section (Kimi OAuth or Meta Device OAuth) -->
    <div v-if="platform === 'kimi' || metaMode === 'device'" class="space-y-4">
      <!-- Kimi Domain Selector -->
      <div v-if="platform === 'kimi'">
        <label class="input-label">{{ t('admin.accounts.oauth.kimi.domainLabel') }}</label>
        <div class="mt-1.5 grid grid-cols-2 gap-2.5">
          <button
            type="button"
            :disabled="polling || loading"
            @click="$emit('update:kimiDomain', 'kimi.com')"
            :class="[
              'rounded-lg border-2 px-3 py-2 text-left text-sm transition-all',
              kimiDomain === 'kimi.com'
                ? 'border-pink-500 bg-pink-50 text-pink-700 dark:bg-pink-900/20 dark:text-pink-300'
                : 'border-gray-200 text-gray-700 hover:border-pink-300 dark:border-dark-600 dark:text-gray-300'
            ]"
          >
            <div class="font-medium">kimi.com</div>
            <div class="text-xs opacity-75">{{ t('admin.accounts.oauth.kimi.domainGlobal') }}</div>
          </button>
          <button
            type="button"
            :disabled="polling || loading"
            @click="$emit('update:kimiDomain', 'kimi.ai')"
            :class="[
              'rounded-lg border-2 px-3 py-2 text-left text-sm transition-all',
              kimiDomain === 'kimi.ai'
                ? 'border-pink-500 bg-pink-50 text-pink-700 dark:bg-pink-900/20 dark:text-pink-300'
                : 'border-gray-200 text-gray-700 hover:border-pink-300 dark:border-dark-600 dark:text-gray-300'
            ]"
          >
            <div class="font-medium">kimi.ai</div>
            <div class="text-xs opacity-75">{{ t('admin.accounts.oauth.kimi.domainAlt') }}</div>
          </button>
        </div>
      </div>

      <!-- Start / Regenerate Button -->
      <div class="rounded-lg border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-700/50">
        <p class="text-sm text-gray-600 dark:text-gray-300">
          {{
            platform === 'kimi'
              ? t('admin.accounts.oauth.kimi.deviceDesc')
              : t('admin.accounts.oauth.meta.deviceDesc')
          }}
        </p>

        <div class="mt-3 flex flex-wrap items-center gap-3">
          <button
            type="button"
            data-testid="device-flow-start-btn"
            :disabled="loading"
            class="btn btn-primary"
            @click="$emit('start')"
          >
            <svg v-if="loading" class="-ml-1 mr-2 h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
            </svg>
            {{
              userCode
                ? t('admin.accounts.oauth.deviceFlow.restart')
                : t('admin.accounts.oauth.deviceFlow.start')
            }}
          </button>

          <button
            v-if="polling"
            type="button"
            class="btn btn-secondary"
            @click="$emit('stop')"
          >
            {{ t('common.cancel') }}
          </button>
        </div>
      </div>

      <!-- Verification Code & Link Card -->
      <div
        v-if="userCode"
        class="space-y-4 rounded-lg border-2 border-primary-500/30 bg-primary-50/40 p-4 dark:bg-primary-900/10"
        data-testid="device-flow-code-card"
      >
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <div class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
              {{ t('admin.accounts.oauth.deviceFlow.userCodeLabel') }}
            </div>
            <div class="mt-1 font-mono text-2xl font-bold tracking-widest text-gray-900 dark:text-white">
              {{ userCode }}
            </div>
          </div>
          <div class="flex items-center gap-2">
            <button type="button" class="btn btn-secondary btn-sm" @click="copyCode">
              {{ copied ? t('common.copied') : t('common.copy') }}
            </button>
            <a
              :href="verificationUriComplete || verificationUri"
              target="_blank"
              rel="noopener noreferrer"
              class="btn btn-primary btn-sm"
            >
              {{ t('admin.accounts.oauth.deviceFlow.openVerificationPage') }}
            </a>
          </div>
        </div>

        <div class="text-xs text-gray-500 dark:text-gray-400 break-all">
          {{ t('admin.accounts.oauth.deviceFlow.verificationUrlLabel') }}:
          <a
            :href="verificationUriComplete || verificationUri"
            target="_blank"
            rel="noopener noreferrer"
            class="text-primary-600 underline dark:text-primary-400"
          >
            {{ verificationUriComplete || verificationUri }}
          </a>
        </div>

        <div
          v-if="polling"
          class="flex items-center gap-2 text-sm text-primary-600 dark:text-primary-400"
        >
          <svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
          </svg>
          <span>{{ t('admin.accounts.oauth.deviceFlow.waitingApproval') }}</span>
        </div>
      </div>
    </div>

    <!-- Meta Direct Token (dca:... or API Key) Section -->
    <div v-else-if="platform === 'meta' && metaMode === 'token'" class="space-y-3">
      <p class="text-sm text-gray-600 dark:text-gray-300">
        {{ t('admin.accounts.oauth.meta.directTokenDesc') }}
      </p>
      <textarea
        v-model="directToken"
        rows="4"
        data-testid="meta-direct-token-input"
        class="input font-mono text-xs"
        :placeholder="t('admin.accounts.oauth.meta.directTokenPlaceholder')"
      />
      <div class="flex justify-end">
        <button
          type="button"
          data-testid="meta-direct-token-submit"
          :disabled="loading || !directToken.trim()"
          class="btn btn-primary"
          @click="$emit('validate-token', directToken.trim())"
        >
          <svg v-if="loading" class="-ml-1 mr-2 h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24">
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
          </svg>
          {{
            loading
              ? t('admin.accounts.oauth.verifying')
              : t('admin.accounts.oauth.completeAuth')
          }}
        </button>
      </div>
    </div>

    <!-- Error banner -->
    <div
      v-if="error"
      class="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-800/50 dark:bg-red-900/20 dark:text-red-300"
    >
      {{ error }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { KimiOAuthDomain } from '@/api/admin/kimi'

interface Props {
  platform: 'kimi' | 'meta'
  kimiDomain?: KimiOAuthDomain
  userCode?: string
  verificationUri?: string
  verificationUriComplete?: string
  polling?: boolean
  loading?: boolean
  error?: string
}

const props = withDefaults(defineProps<Props>(), {
  kimiDomain: 'kimi.com',
  userCode: '',
  verificationUri: '',
  verificationUriComplete: '',
  polling: false,
  loading: false,
  error: ''
})

defineEmits<{
  start: []
  stop: []
  'update:kimiDomain': [value: KimiOAuthDomain]
  'validate-token': [token: string]
}>()

const { t } = useI18n()
const metaMode = ref<'device' | 'token'>('device')
const directToken = ref('')
const copied = ref(false)

const copyCode = async () => {
  if (!props.userCode) return
  try {
    await navigator.clipboard.writeText(props.userCode)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch {
    // ignore clipboard errors in headless/test environments
  }
}

defineExpose({
  reset: () => {
    metaMode.value = 'device'
    directToken.value = ''
    copied.value = false
  }
})
</script>
