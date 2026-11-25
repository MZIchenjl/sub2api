/**
 * Admin Devin API endpoints
 * Handles Devin OAuth PKCE authorization and token/API key validation for administrators
 */

import { apiClient } from '../client'

export interface DevinAuthUrlRequest {
  proxy_id?: number
  redirect_uri?: string
}

export interface DevinAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
}

export interface DevinExchangeCodeRequest {
  session_id: string
  code: string
  state?: string
  proxy_id?: number
}

export interface DevinValidateTokenRequest {
  token: string
  base_url?: string
  proxy_id?: number
}

export interface DevinTokenInfo {
  session_token: string
  email?: string
  user_name?: string
  user_id?: string
  org_id?: string
  org_name?: string
  team_id?: string
  plan?: string
  base_url?: string
  daily_quota_remaining_percent?: number
  weekly_quota_remaining_percent?: number
  daily_quota_reset_at?: string
  weekly_quota_reset_at?: string
  [key: string]: unknown
}

export async function generateAuthUrl(
  payload: DevinAuthUrlRequest
): Promise<DevinAuthUrlResponse> {
  const { data } = await apiClient.post<DevinAuthUrlResponse>(
    '/admin/devin/oauth/auth-url',
    payload
  )
  return data
}

export async function exchangeCode(
  payload: DevinExchangeCodeRequest
): Promise<DevinTokenInfo> {
  const { data } = await apiClient.post<DevinTokenInfo>(
    '/admin/devin/oauth/exchange-code',
    payload
  )
  return data
}

export async function validateToken(
  payload: DevinValidateTokenRequest
): Promise<DevinTokenInfo> {
  const { data } = await apiClient.post<DevinTokenInfo>(
    '/admin/devin/oauth/validate-token',
    payload
  )
  return data
}

export default { generateAuthUrl, exchangeCode, validateToken }
