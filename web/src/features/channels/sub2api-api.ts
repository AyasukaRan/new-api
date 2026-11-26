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
  keys: Sub2APIKey[]
}

export type Sub2APILoginResult =
  | { requires_2fa: false; account_id: number }
  | { requires_2fa: true; flow: string; email_masked: string }

export type Sub2APIAction =
  | 'login'
  | 'import'
  | 'refresh'
  | 'disconnect'
  | 'sync'
  | 'keys'

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
  action: Sub2APIAction | 'login/2fa',
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
