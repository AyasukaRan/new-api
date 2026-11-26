/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

export interface Sub2APIAccount {
  id: number
  origin: string
  email: string
  upstream_user_id: number
  expires_at: number
  has_refresh_token: boolean
  needs_login: boolean
  last_sync_at: number
  last_error: string
  has_proxy: boolean
  channel_ids: number[]
  channels: { id: number; name: string }[]
  legacy: boolean
}

export interface Sub2APIKey {
  key_identity: string
  key_index: number
  key_preview: string
  account_id?: number
  upstream_api_key_id?: number
  upstream_group_id?: number
  sync_rates: boolean
  sync_bills: boolean
  bills_pending?: number
  bills_synced?: number
  bill_last_synced_at?: number
  rate?: {
    resolved_rate_multiplier: number
    effective_rate_multiplier: number
    peak_rate_enabled: boolean
    peak_start?: string
    peak_end?: string
    peak_rate_multiplier?: number
    timezone?: string
    observed_at: string
  }
  last_sync_at: number
  last_error: string
}

export interface Sub2APIStatus {
  encryption_ready: boolean
  management_allowed: boolean
  accounts: Sub2APIAccount[]
  available_accounts: Sub2APIAccount[]
  keys: Sub2APIKey[]
}

export type Sub2APILoginResult =
  | { requires_2fa: false; account_id: number }
  | { requires_2fa: true; flow: string; email_masked: string }

export type Sub2APIAction = 'attach' | 'detach' | 'sync' | 'keys'

export type Sub2APIAccountAction =
  | 'login'
  | 'import'
  | 'refresh'
  | 'disconnect'
  | 'sync'

export interface Sub2APIInventory {
  account_id: number
  cached: boolean
  snapshot: null | {
    synced_at: number
    keys: {
      id: number
      name: string
      masked_key: string
      group_id: number | null
      status: string
      expires_at: string | null
    }[]
    groups: {
      id: number
      name: string
      description: string
      platform: string
      group_rate_multiplier: number
      user_rate_multiplier: number | null
      resolved_rate_multiplier: number
      peak_rate_enabled: boolean
      peak_start: string
      peak_end: string
      peak_rate_multiplier: number
      rate?: Sub2APIKey['rate']
    }[]
  }
  last_attempt_at: number
  next_attempt_at: number
  last_error: string
  records: {
    at: number
    success: boolean
    error_code: string
    error_message: string
    upstream_status: number
    keys: number
    groups: number
  }[]
}

export function getSub2APIInventory(accountId: number, signal?: AbortSignal) {
  return authResult<Sub2APIInventory>(
    api.get(`/api/sub2api/accounts/${accountId}/inventory`, {
      ...authRequestOptions,
      signal,
      disableDuplicate: true,
    })
  )
}

export type Sub2APIAccountsStatus = Pick<
  Sub2APIStatus,
  'accounts' | 'encryption_ready' | 'management_allowed'
>

export function getSub2APIAccounts(signal?: AbortSignal) {
  return authResult<Sub2APIAccountsStatus>(
    api.get('/api/sub2api/accounts', {
      ...authRequestOptions,
      signal,
      disableDuplicate: true,
    })
  )
}

export function submitSub2APIAccountAction<T>(
  action: Sub2APIAccountAction | 'login/2fa',
  data: Record<string, unknown>,
  proof: string | undefined,
  signal: AbortSignal
) {
  return authResult<T>(
    api.post(`/api/sub2api/accounts/${action}`, data, {
      ...authRequestOptions,
      signal,
      singleUseAuthorization: true,
      headers: proof ? { 'X-Security-Proof': proof } : undefined,
    })
  )
}

export function getSub2APIStatus(channelId: number, signal?: AbortSignal) {
  return authResult<Sub2APIStatus>(
    api.get(`/api/channel/${channelId}/sub2api`, {
      ...authRequestOptions,
      signal,
      disableDuplicate: true,
    })
  )
}

/** Only encrypted credentials cross this boundary. Proofs are never replayed. */
export function submitSub2APIAction<T>(
  channelId: number,
  action: Sub2APIAction,
  data: Record<string, unknown>,
  proof: string | undefined,
  signal: AbortSignal
) {
  const config = {
    ...authRequestOptions,
    signal,
    singleUseAuthorization: true,
    headers: proof ? { 'X-Security-Proof': proof } : undefined,
  }
  const url = `/api/channel/${channelId}/sub2api/${action}`
  return authResult<T>(
    action === 'keys' ? api.put(url, data, config) : api.post(url, data, config)
  )
}
