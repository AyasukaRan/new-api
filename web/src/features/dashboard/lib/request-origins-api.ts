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
import { createParser, type EventSourceMessage } from 'eventsource-parser'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export interface RequestOriginPoint {
  location_id: string
  country_code: string
  country: string
  region: string
  city: string
  latitude: number
  longitude: number
  count: number
  last_seen: number
}

export interface RequestOriginsSnapshot {
  points: RequestOriginPoint[]
  total: number
  located: number
  unmapped: number
  private: number
  invalid: number
  truncated: boolean
  sample_start: number
  cursor: string
  geoip_available: boolean
  geoip_updated_at: number
}

export interface RequestOriginsParams {
  start_timestamp: number
  end_timestamp: number
  username?: string
}

export function isRequestOriginPoint(
  value: unknown
): value is RequestOriginPoint {
  if (!value || typeof value !== 'object') return false
  const point = value as RequestOriginPoint
  return (
    typeof point.location_id === 'string' &&
    typeof point.country_code === 'string' &&
    typeof point.country === 'string' &&
    typeof point.region === 'string' &&
    typeof point.city === 'string' &&
    Number.isFinite(point.latitude) &&
    Number.isFinite(point.longitude) &&
    Math.abs(point.latitude) <= 90 &&
    Math.abs(point.longitude) <= 180 &&
    Number.isSafeInteger(point.count) &&
    point.count >= 0 &&
    Number.isFinite(point.last_seen)
  )
}

export async function getRequestOrigins(
  params: RequestOriginsParams,
  isAdmin: boolean,
  signal?: AbortSignal
): Promise<RequestOriginsSnapshot> {
  const response = await api.get<{
    success: boolean
    data?: RequestOriginsSnapshot
  }>(isAdmin ? '/api/data/origins' : '/api/data/origins/self', {
    params,
    signal,
  })
  const data = requireServerSuccess(response.data).data
  if (
    !data ||
    !Array.isArray(data.points) ||
    !data.points.every(isRequestOriginPoint) ||
    typeof data.cursor !== 'string' ||
    typeof data.geoip_available !== 'boolean' ||
    ![
      data.total,
      data.located,
      data.unmapped,
      data.private,
      data.invalid,
    ].every((value) => Number.isSafeInteger(value) && value >= 0)
  ) {
    throw new Error('Invalid response')
  }
  return data
}

export async function streamRequestOrigins(
  params: Omit<RequestOriginsParams, 'end_timestamp'>,
  isAdmin: boolean,
  cursor: string,
  signal: AbortSignal,
  onEvent: (event: EventSourceMessage) => void
) {
  const endpoint = isAdmin
    ? '/api/data/origins/stream'
    : '/api/data/origins/self/stream'
  const response = await api.get<ReadableStream<Uint8Array>>(endpoint, {
    params: { ...params, cursor },
    headers: { Accept: 'text/event-stream' },
    timeout: 75_000,
    adapter: 'fetch',
    responseType: 'stream',
    signal,
    disableDuplicate: true,
    skipErrorHandler: true,
  })
  const reader = response.data.getReader()
  const decoder = new TextDecoder()
  const parser = createParser({ onEvent })
  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read()
      if (done || signal.aborted) break
      parser.feed(decoder.decode(value, { stream: true }))
    }
  } finally {
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}
