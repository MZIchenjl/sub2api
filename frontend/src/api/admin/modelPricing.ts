import { apiClient } from '../client'

export type PricingSyncOverwriteMode = 'skip_locked' | 'overwrite_all'

export interface PricingSyncConfig {
  auto_sync_enabled: boolean
  sync_interval_hours: number
  models_dev_url: string
}

export interface PricingSyncMetadata {
  last_sync_at?: string
  last_sync_status: 'never' | 'success' | 'failed' | string
  last_sync_error?: string
  cache_hash?: string
  channel_count: number
  model_count: number
  raw_entry_count: number
  last_skipped_locked?: number
  last_overwritten_count?: number
}

export interface PricingSyncStatus {
  config: PricingSyncConfig
  meta: PricingSyncMetadata
  default_override_count: number
  channel_override_count: number
  locked_channel_count: number
  unlocked_custom_count: number
  group_override_counts: Record<string, number>
  cache_loaded: boolean
}

export interface TokenPricingPerMillion {
  display_name?: string
  input_per_million: number
  output_per_million: number
  cache_read_per_million: number
  cache_write_per_million: number
  cache_write_1h_per_million?: number | null
  cache_storage_per_million_per_hour?: number
  long_context_threshold?: number
  long_context_input_multiplier?: number
  long_context_output_multiplier?: number
}

export interface AccountChannelPriceEntry {
  channel_id: string
  channel_name: string
  platform: string
  raw_model_id: string
  is_default: boolean
  is_overridden: boolean
  is_locked: boolean
  pricing: TokenPricingPerMillion
  synced_pricing?: TokenPricingPerMillion
  updated_at?: string
}

export interface SupportedPlatformSummary {
  id: string
  name: string
  model_count: number
}

export type ModelPricingSource =
  | 'default_auto'
  | 'default_custom'
  | 'inherited_default'
  | 'group_custom'

export interface ModelPricingListItem {
  model_id: string
  display_name: string
  platform: string
  platforms: string[]
  family?: string
  release_date?: string
  source: ModelPricingSource
  is_group_overridden: boolean
  is_default_modified: boolean
  is_default_locked: boolean
  is_custom_only: boolean
  default_channel_id?: string
  default_channel_name?: string
  channel_prices: AccountChannelPriceEntry[]
  effective_pricing: TokenPricingPerMillion
  default_pricing?: TokenPricingPerMillion
  group_override_count: number
  channel_override_count: number
  locked_channel_count: number
  updated_at?: string
}

export interface ModelPricingListResult {
  items: ModelPricingListItem[]
  total: number
  page: number
  page_size: number
  group_platform?: string
  platforms: SupportedPlatformSummary[]
}

export interface ModelPricingListParams {
  group_id?: number | null
  platform?: string
  search?: string
  filter?: 'all' | 'overridden' | 'custom' | 'locked'
  page?: number
  page_size?: number
}

export interface UpsertModelPricingOverrideRequest {
  group_id?: number | null
  channel_id?: string
  model_id: string
  platform?: string
  display_name?: string
  locked?: boolean
  input_per_million: number
  output_per_million: number
  cache_read_per_million: number
  cache_write_per_million: number
  cache_write_1h_per_million?: number | null
  long_context_threshold?: number
  long_context_input_multiplier?: number
  long_context_output_multiplier?: number
}

export interface ResetOverrideScope {
  groupId?: number | null
  channelId?: string
}

export const modelPricingAPI = {
  async getStatus(): Promise<PricingSyncStatus> {
    const { data } = await apiClient.get<PricingSyncStatus>('/admin/model-pricing/status')
    return data
  },

  async updateSyncConfig(payload: PricingSyncConfig): Promise<PricingSyncStatus> {
    const { data } = await apiClient.put<PricingSyncStatus>(
      '/admin/model-pricing/sync-config',
      payload
    )
    return data
  },

  async syncNow(
    overwriteMode: PricingSyncOverwriteMode = 'skip_locked'
  ): Promise<PricingSyncStatus> {
    const { data } = await apiClient.post<PricingSyncStatus>('/admin/model-pricing/sync', {
      overwrite_mode: overwriteMode
    })
    return data
  },

  async listModels(params: ModelPricingListParams = {}): Promise<ModelPricingListResult> {
    const query: Record<string, string | number> = {}
    if (params.group_id && params.group_id > 0) query.group_id = params.group_id
    if (params.platform && params.platform !== 'all') query.platform = params.platform
    if (params.search?.trim()) query.search = params.search.trim()
    if (params.filter && params.filter !== 'all') query.filter = params.filter
    if (params.page) query.page = params.page
    if (params.page_size) query.page_size = params.page_size

    const { data } = await apiClient.get<ModelPricingListResult>('/admin/model-pricing/models', {
      params: query
    })
    return data
  },

  async upsertOverride(payload: UpsertModelPricingOverrideRequest): Promise<{ message: string }> {
    const { data } = await apiClient.put<{ message: string }>(
      '/admin/model-pricing/override',
      payload
    )
    return data
  },

  async resetOverride(
    modelId: string,
    scope: ResetOverrideScope = {}
  ): Promise<{ message: string }> {
    const params: Record<string, string | number> = { model_id: modelId }
    if (scope.groupId && scope.groupId > 0) params.group_id = scope.groupId
    if (scope.channelId) params.channel_id = scope.channelId
    const { data } = await apiClient.delete<{ message: string }>('/admin/model-pricing/override', {
      params
    })
    return data
  },

  async setChannelLock(
    modelId: string,
    channelId: string,
    locked: boolean
  ): Promise<{ message: string }> {
    const { data } = await apiClient.put<{ message: string }>('/admin/model-pricing/lock', {
      model_id: modelId,
      channel_id: channelId,
      locked
    })
    return data
  }
}

export default modelPricingAPI
