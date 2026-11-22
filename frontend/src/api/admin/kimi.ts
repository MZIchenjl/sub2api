/**
 * Admin Kimi Code OAuth API endpoints
 * RFC 8628 Device Authorization Grant for Kimi Code (kimi.com / kimi.ai)
 */

import { apiClient } from '../client'

export type KimiOAuthDomain = 'kimi.com' | 'kimi.ai'

export interface KimiDeviceFlowStartRequest {
  domain?: KimiOAuthDomain
  proxy_id?: number
}

export interface KimiDeviceFlowStartResponse {
  session_id: string
  user_code: string
  verification_uri: string
  verification_uri_complete: string
  expires_in: number
  interval: number
  domain: KimiOAuthDomain
}

export interface KimiDeviceFlowPollRequest {
  session_id: string
}

export interface KimiTokenInfo {
  access_token: string
  refresh_token: string
  token_type?: string
  scope?: string
  expires_in?: number
  expires_at?: number
  device_id?: string
  domain: KimiOAuthDomain
  api_base_url: string
  [key: string]: unknown
}

export interface KimiDeviceFlowPollResponse {
  status: 'pending' | 'slow_down' | 'completed' | 'expired'
  interval?: number
  token?: KimiTokenInfo
}

export interface KimiRefreshTokenRequest {
  refresh_token: string
  domain?: KimiOAuthDomain
  device_id?: string
  proxy_id?: number
}

export async function startDeviceFlow(
  payload: KimiDeviceFlowStartRequest
): Promise<KimiDeviceFlowStartResponse> {
  const { data } = await apiClient.post<KimiDeviceFlowStartResponse>(
    '/admin/kimi/oauth/device-code',
    payload
  )
  return data
}

export async function pollDeviceFlow(
  payload: KimiDeviceFlowPollRequest
): Promise<KimiDeviceFlowPollResponse> {
  const { data } = await apiClient.post<KimiDeviceFlowPollResponse>(
    '/admin/kimi/oauth/poll',
    payload
  )
  return data
}

export async function refreshToken(
  payload: KimiRefreshTokenRequest
): Promise<KimiTokenInfo> {
  const { data } = await apiClient.post<KimiTokenInfo>(
    '/admin/kimi/oauth/refresh',
    payload
  )
  return data
}

export default { startDeviceFlow, pollDeviceFlow, refreshToken }
