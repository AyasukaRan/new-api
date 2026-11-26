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
import { useInfiniteQuery } from '@tanstack/react-query'
import { useId, useMemo, useRef, useState } from 'react'

import { createServerError } from '@/lib/server-error-message'

import { getAllLogs, getUserLogs } from '../api'
import type { UsageLog } from '../data/schema'
import type { GetLogsParams } from '../types'

interface HistorySession {
  id: number
  source: GetLogsParams
  head: UsageLog[]
  tail?: UsageLog
  end: number
  total: number
}

interface HistoryCursor {
  page: number
  end: number
  total: number
  tail?: UsageLog
}

export function useUsageLogHistory(
  filters: GetLogsParams | undefined,
  isAdmin: boolean
) {
  const tableId = useId()
  const sequence = useRef(0)
  const [session, setSession] = useState<HistorySession>()
  const active = filters && session?.source === filters ? session : undefined
  if (session && !active) setSession(undefined)
  const pageSize = filters?.page_size ?? 100
  const basePage = filters?.p ?? 1
  const query = useInfiniteQuery({
    queryKey: ['usage-log-history', tableId, active?.id],
    enabled: !!active,
    initialPageParam: {
      page: basePage + 1,
      end: active?.end ?? 0,
      total: active?.total ?? 0,
      tail: active?.tail,
    } as HistoryCursor,
    queryFn: async ({ pageParam, signal }) => {
      if (!active) throw new Error('Inactive log history')
      let tail = pageParam.tail
      let { page, end } = pageParam
      let seekingTail = false
      for (;;) {
        const response = await (isAdmin ? getAllLogs : getUserLogs)(
          { ...active.source, end_timestamp: end, p: page },
          signal
        )
        if (!response.success || !response.data) {
          throw createServerError(response)
        }
        const raw = response.data.items as UsageLog[]
        const { total } = response.data
        const tailKey = tail?.live_key
        const overlap = tailKey
          ? raw.findIndex((row) => row.live_key === tailKey)
          : -1
        // Enough commits in the boundary second can push a whole unseen page
        // ahead of the reader. Find the old tail even when no keys overlap.
        if (
          !seekingTail &&
          tail?.live_key &&
          raw.length &&
          overlap < 0 &&
          raw[0].created_at >= tail.created_at &&
          total - pageParam.total >= pageSize
        ) {
          seekingTail = true
          page = 1
          end = tail.created_at
          continue
        }
        let items = raw
        if (overlap >= 0) {
          items = raw.slice(overlap + 1)
          tail = undefined
        } else if (seekingTail && tail) {
          const boundaryTime = tail.created_at
          items = raw.filter((row) => row.created_at < boundaryTime)
        }
        const hasMore = raw.length >= pageSize && page * pageSize < total
        page++
        if (items.length || !hasMore) {
          const next: HistoryCursor | undefined = hasMore
            ? { page, end, total, tail: items.at(-1) ?? tail }
            : undefined
          return { items, next }
        }
      }
    },
    getNextPageParam: (last) => last.next,
    staleTime: Infinity,
    gcTime: 0,
    retry: false,
  })
  const rows = useMemo(() => {
    if (!active) return undefined
    if (!query.data) return active.head
    const result = [...active.head]
    const seen = new Set(result.map((row) => row.live_key))
    for (const page of query.data.pages) {
      for (const row of page.items) {
        if (row.live_key && seen.has(row.live_key)) continue
        seen.add(row.live_key)
        result.push(row)
      }
    }
    return result
  }, [active, query.data])

  function loadOlder(
    head: UsageLog[],
    total: number,
    committedHead: UsageLog[] = head
  ) {
    if (!filters || query.isFetching) return
    if (active) {
      if (!query.data && query.isError) void query.refetch()
      else if (query.hasNextPage) {
        void query.fetchNextPage({ cancelRefetch: false })
      }
      return
    }
    if (!committedHead.length || basePage * pageSize >= total) return
    // Live page one may have advanced since its initial snapshot. Anchor older
    // pages to the visible head. Manual pages keep their original date range.
    const end =
      basePage === 1
        ? Math.min(
            filters.end_timestamp ?? Infinity,
            committedHead[0].created_at
          )
        : (filters.end_timestamp ?? committedHead[0].created_at)
    // Temporary active rows are visible, but cannot anchor database pagination.
    const next = {
      id: ++sequence.current,
      source: filters,
      end,
      head,
      tail: committedHead.at(-1),
      total,
    }
    setSession((previous) => (previous?.source === filters ? previous : next))
  }

  return {
    rows,
    isBrowsingHistory: !!active,
    loadOlder,
    clearHistory: () => setSession(undefined),
  }
}
