/**
 * Admin Model Pricing API endpoints (models.dev sync, cached provider switching,
 * and default / per-group pricing overrides).
 */

import { apiClient } from '../client'

export interface PricingSyncConfig {
  auto_sync_enabled: boolean
  sync_interval_hours: number
  models_dev_url: string
}

export interface PricingSyncMetadata {
  last_sync_at?: string | null
  last_sync_status: 'never' | 'success' | 'failed' | string
  last_sync_error?: string
  cache_hash?: string
  provider_count: number
  model_group_count: number
  raw_entry_count: number
}

export interface PricingSyncStatus {
  config: PricingSyncConfig
  meta: PricingSyncMetadata
  default_override_count: number
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
  long_context_threshold?: number
  long_context_input_multiplier?: number
  long_context_output_multiplier?: number
}

export interface ModelsDevCandidate {
  candidate_key: string
  provider_id: string
  provider_name: string
  raw_model_id: string
  display_name: string
  family?: string
  release_date?: string
  status?: string
  context_limit?: number
  output_limit?: number
  is_recommended: boolean
  pricing: TokenPricingPerMillion
}

export interface ModelsDevProviderSummary {
  id: string
  name: string
  model_count: number
}

export type ModelPricingItemSource =
  | 'default_auto'
  | 'default_provider'
  | 'default_custom'
  | 'inherited_default'
  | 'group_provider'
  | 'group_custom'

export interface ModelPricingListItem {
  model_id: string
  display_name: string
  family?: string
  release_date?: string
  source: ModelPricingItemSource
  is_group_overridden: boolean
  is_default_modified: boolean
  is_custom_only: boolean
  active_provider_id?: string
  active_provider_name?: string
  active_raw_model_id?: string
  active_candidate_key?: string
  effective_pricing: TokenPricingPerMillion
  default_pricing?: TokenPricingPerMillion | null
  candidates: ModelsDevCandidate[]
  candidate_count: number
  group_override_count: number
  updated_at?: string | null
}

export interface ModelPricingListParams {
  group_id?: number
  search?: string
  provider?: string
  filter?: 'all' | 'overridden' | 'custom' | 'multi_provider'
  page?: number
  page_size?: number
}

export interface ModelPricingListResult {
  items: ModelPricingListItem[]
  total: number
  page: number
  page_size: number
  providers: ModelsDevProviderSummary[]
}

export interface SelectCachedProviderRequest {
  group_id?: number | null
  model_id: string
  candidate_key: string
}

export interface UpsertModelPricingOverrideRequest extends TokenPricingPerMillion {
  group_id?: number | null
  model_id: string
}

export async function getStatus(): Promise<PricingSyncStatus> {
  const { data } = await apiClient.get<PricingSyncStatus>('/admin/model-pricing/status')
  return data
}

export async function updateSyncConfig(payload: PricingSyncConfig): Promise<PricingSyncStatus> {
  const { data } = await apiClient.put<PricingSyncStatus>('/admin/model-pricing/sync-config', payload)
  return data
}

export async function syncNow(): Promise<PricingSyncStatus> {
  const { data } = await apiClient.post<PricingSyncStatus>('/admin/model-pricing/sync')
  return data
}

export async function listModels(params: ModelPricingListParams = {}): Promise<ModelPricingListResult> {
  const { data } = await apiClient.get<ModelPricingListResult>('/admin/model-pricing/models', {
    params
  })
  return data
}

export async function selectProvider(payload: SelectCachedProviderRequest): Promise<{ message: string }> {
  const { data } = await apiClient.put<{ message: string }>('/admin/model-pricing/select-provider', payload)
  return data
}

export async function upsertOverride(payload: UpsertModelPricingOverrideRequest): Promise<{ message: string }> {
  const { data } = await apiClient.put<{ message: string }>('/admin/model-pricing/override', payload)
  return data
}

export async function resetOverride(modelId: string, groupId?: number | null): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>('/admin/model-pricing/override', {
    params: {
      model_id: modelId,
      ...(groupId && groupId > 0 ? { group_id: groupId } : {})
    }
  })
  return data
}

const modelPricingAPI = {
  getStatus,
  updateSyncConfig,
  syncNow,
  listModels,
  selectProvider,
  upsertOverride,
  resetOverride
}

export default modelPricingAPI
