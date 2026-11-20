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
import { useQuery } from '@tanstack/react-query'

import dayjs from '@/lib/dayjs'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getUserQuotaDates } from '../api'
import type { QuotaDataItem } from '../types'

export type PersonalUsageRange = '24h' | '7d'
export type PersonalUsageMetric = 'requests' | 'tokens' | 'usage'

const HOUR = 3600

type UsageBucket = Record<PersonalUsageMetric, number> & {
  time: number
  cacheRead: number | null
  cacheWrite: number | null
}

// Older responses may omit cache measurements. A partial sum would understate usage.
function addReportedTokens(sum: number | null, tokens: number | undefined) {
  return sum === null || tokens == null ? null : sum + tokens
}

export function startOfUsageWindow(
  range: PersonalUsageRange,
  end: number
): number {
  // The gateway stores one row per model and UTC hour.
  if (range === '24h') return end - (end % HOUR) - 23 * HOUR
  return dayjs(end * 1000)
    .startOf('day')
    .subtract(6, 'day')
    .unix()
}

export function summarizePersonalUsage(
  range: PersonalUsageRange,
  start: number,
  end: number,
  rows: QuotaDataItem[]
) {
  const first = dayjs(start * 1000)
  const buckets = Array.from({ length: range === '24h' ? 24 : 7 }, (_, i) => {
    if (range === '24h') {
      return { start: start + i * HOUR, end: start + (i + 1) * HOUR }
    }
    return {
      start: first.add(i, 'day').unix(),
      end: first.add(i + 1, 'day').unix(),
    }
  })
  return summarizeUsageRows(rows, {
    start_timestamp: start,
    end_timestamp: end,
    buckets,
  })
}

export function summarizeUsageRows(
  rows: readonly QuotaDataItem[],
  domain: {
    start_timestamp: number
    end_timestamp: number
    buckets: ReadonlyArray<{ start: number; end: number }>
  }
) {
  const { start_timestamp: start, end_timestamp: end, buckets } = domain
  const points: UsageBucket[] = buckets.map((bucket) => ({
    time: bucket.start,
    requests: 0,
    tokens: 0,
    usage: 0,
    cacheRead: 0,
    cacheWrite: 0,
  }))
  const totals: Omit<UsageBucket, 'time'> = {
    requests: 0,
    tokens: 0,
    usage: 0,
    cacheRead: 0,
    cacheWrite: 0,
  }
  const windowRows: QuotaDataItem[] = []
  for (const row of rows) {
    if (
      !Number.isFinite(row.created_at) ||
      row.created_at < start ||
      row.created_at > end
    ) {
      continue
    }
    let low = 0
    let high = buckets.length
    while (low < high) {
      const mid = Math.floor((low + high) / 2)
      if (buckets[mid].end <= row.created_at) low = mid + 1
      else high = mid
    }
    if (!buckets[low] || row.created_at < buckets[low].start) continue
    const point = points[low]
    const requests = row.count ?? 0
    const tokens = row.token_used ?? 0
    const usage = row.quota ?? 0
    point.requests += requests
    point.tokens += tokens
    point.usage += usage
    point.cacheRead = addReportedTokens(point.cacheRead, row.cache_read_tokens)
    point.cacheWrite = addReportedTokens(
      point.cacheWrite,
      row.cache_creation_tokens
    )
    totals.requests += requests
    totals.tokens += tokens
    totals.usage += usage
    totals.cacheRead = addReportedTokens(
      totals.cacheRead,
      row.cache_read_tokens
    )
    totals.cacheWrite = addReportedTokens(
      totals.cacheWrite,
      row.cache_creation_tokens
    )
    windowRows.push(row)
  }
  return { rows: windowRows, points, totals, start, end }
}

export type UsageSeries = ReturnType<typeof summarizeUsageRows>

export function usePersonalUsage(range: PersonalUsageRange = '24h') {
  const userId = useAuthStore((state) => state.auth.user?.id)
  return useQuery({
    queryKey: ['dashboard', 'personal-usage', userId, range],
    enabled: userId != null,
    staleTime: 60_000,
    queryFn: async () => {
      const end = Math.floor(Date.now() / 1000)
      const start = startOfUsageWindow(range, end)
      const response = requireServerSuccess(
        await getUserQuotaDates({
          start_timestamp: start,
          end_timestamp: end,
          default_time: range === '24h' ? 'hour' : 'day',
        })
      )
      if (response.data !== null && !Array.isArray(response.data)) {
        throw new Error('Invalid response')
      }
      return summarizePersonalUsage(range, start, end, response.data ?? [])
    },
  })
}
