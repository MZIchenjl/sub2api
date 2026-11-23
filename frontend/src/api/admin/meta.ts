/**
 * Admin Meta Muse Code OAuth API endpoints
 * RFC 8628 Device Authorization Grant + API key minting / token validation
 */

import { apiClient } from '../client'

export interface MetaDeviceFlowStartRequest {
  proxy_id?: number
}

export interface MetaDeviceFlowStartResponse {
  session_id: string
  user_code: string
  verification_uri: string
  verification_uri_complete: string
  expires_in: number
  interval: number
}

export interface MetaDeviceFlowPollRequest {
  session_id: string
}

export interface MetaTokenInfo {
  api_key: string
  dca_token?: string
  token_type?: string
  scope?: string
  expires_in?: number
  expires_at?: number
  base_url: string
  [key: string]: unknown
}

export interface MetaDeviceFlowPollResponse {
  status: 'pending' | 'slow_down' | 'completed' | 'expired'
  interval?: number
  token?: MetaTokenInfo
}

export interface MetaValidateTokenRequest {
  token: string
  base_url?: string
  proxy_id?: number
}

export async function startDeviceFlow(
  payload: MetaDeviceFlowStartRequest
): Promise<MetaDeviceFlowStartResponse> {
  const { data } = await apiClient.post<MetaDeviceFlowStartResponse>(
    '/admin/meta/oauth/device-code',
    payload
  )
  return data
}

export async function pollDeviceFlow(
  payload: MetaDeviceFlowPollRequest
): Promise<MetaDeviceFlowPollResponse> {
  const { data } = await apiClient.post<MetaDeviceFlowPollResponse>(
    '/admin/meta/oauth/poll',
    payload
  )
  return data
}

export async function validateToken(
  payload: MetaValidateTokenRequest
): Promise<MetaTokenInfo> {
  const { data } = await apiClient.post<MetaTokenInfo>(
    '/admin/meta/oauth/validate-token',
    payload
  )
  return data
}

export default { startDeviceFlow, pollDeviceFlow, validateToken }
