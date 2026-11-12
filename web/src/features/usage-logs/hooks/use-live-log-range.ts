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
import { useQueryClient } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useEffect, useEffectEvent, useRef } from 'react'

import { handleServerError } from '@/lib/handle-server-error'

import { getRelativeLogTimeRange, type LogTimePreset } from '../lib/time-range'

const route = getRouteApi('/_authenticated/usage-logs/$section')

export function useLiveLogRange(
  preset: LogTimePreset | undefined,
  autoRefresh: boolean
): void {
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const queryClient = useQueryClient()
  const page = search.page ?? 1
  const pendingRefresh = useRef(false)
  const navigating = useRef(false)

  const refresh = useEffectEvent(async () => {
    if (!preset || !autoRefresh || document.hidden || page > 1) {
      pendingRefresh.current = false
      return
    }
    if (navigating.current) return
    if (
      queryClient.isFetching({ queryKey: ['logs'] }) > 0 ||
      queryClient.isFetching({ queryKey: ['usage-logs-stats'] }) > 0
    ) {
      pendingRefresh.current = true
      return
    }
    pendingRefresh.current = false
    const { start, end } = getRelativeLogTimeRange(preset)
    const startTime = start.getTime()
    const endTime = end.getTime()
    if (search.startTime === startTime && search.endTime === endTime) return
    navigating.current = true
    try {
      await navigate({
        search: (previous) => ({
          ...previous,
          timeRange: preset,
          startTime,
          endTime,
        }),
        replace: true,
        resetScroll: false,
      })
    } catch (error) {
      handleServerError(error)
    } finally {
      navigating.current = false
    }
  })

  useEffect(() => {
    if (!preset || !autoRefresh || page > 1) {
      pendingRefresh.current = false
      return
    }
    void refresh()
    const timer = window.setInterval(() => {
      void refresh()
    }, 30_000)
    const onVisibilityChange = () => {
      void refresh()
    }
    document.addEventListener('visibilitychange', onVisibilityChange)
    // A resume during a request refreshes once that request has settled.
    const unsubscribe = queryClient.getQueryCache().subscribe(() => {
      if (pendingRefresh.current) void refresh()
    })
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibilityChange)
      unsubscribe()
      pendingRefresh.current = false
    }
  }, [preset, autoRefresh, page, queryClient])
}
