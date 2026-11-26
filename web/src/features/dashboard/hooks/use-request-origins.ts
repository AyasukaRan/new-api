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
import { isAxiosError } from 'axios'
import { useEffect, useEffectEvent, useMemo, useRef, useState } from 'react'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { isDomesticRequestOrigin } from '../lib/request-origin-layout'
import {
  getRequestOrigins,
  isRequestOriginPoint,
  streamRequestOrigins,
  type RequestOriginPoint,
  type RequestOriginsParams,
} from '../lib/request-origins-api'

export interface RequestOriginPulse extends RequestOriginPoint {
  pulse_id: string
}

type StreamStatus =
  | 'connecting'
  | 'live'
  | 'reconnecting'
  | 'paused'
  | 'history'

export function useRequestOrigins(options: {
  params?: RequestOriginsParams
  hours: number
  paused: boolean
}) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const role = useAuthStore((state) => state.auth.user?.role)
  const isAdmin = Boolean(role && role >= ROLE.ADMIN)
  const [periodEnd] = useState(() => Math.floor(Date.now() / 1000))
  const [activity, setActivity] = useState<{
    scope: string
    pulses: RequestOriginPulse[]
    pendingInternational: RequestOriginPoint[]
    status: StreamStatus
  }>()
  const checkpoint = useRef<{
    scope: string
    cursor: string
    seen: Set<string>
    pendingInternational: RequestOriginPoint[]
    refreshingInternational: boolean
  }>(undefined)
  const requestedStart = options.params?.start_timestamp
  const requestedEnd = options.params?.end_timestamp
  const requestedUsername = isAdmin ? options.params?.username : undefined
  const params = useMemo(
    () => ({
      start_timestamp: requestedStart ?? periodEnd - options.hours * 3600,
      end_timestamp: requestedEnd ?? periodEnd,
      ...(requestedUsername ? { username: requestedUsername } : {}),
    }),
    [requestedStart, requestedEnd, requestedUsername, options.hours, periodEnd]
  )
  const scope = JSON.stringify([userId, role, params, options.hours])
  const live = useMemo(
    () =>
      requestedEnd === undefined ||
      requestedEnd >= Math.floor(Date.now() / 1000) - 90,
    [requestedEnd]
  )
  const query = useQuery({
    queryKey: ['dashboard', 'request-origins', userId, role, params],
    queryFn: ({ signal }) => {
      if (!live) return getRequestOrigins(params, isAdmin, signal)
      const now = Math.floor(Date.now() / 1000)
      return getRequestOrigins(
        {
          ...params,
          start_timestamp: options.params
            ? params.start_timestamp
            : now - options.hours * 3600,
          end_timestamp: now,
        },
        isAdmin,
        signal
      )
    },
    enabled: userId != null,
    staleTime: 30_000,
    refetchInterval: live && !options.paused ? 30_000 : false,
    meta: { errorToast: false },
  })
  const latestSnapshot = useEffectEvent(() => query.data)
  const reload = useEffectEvent(() => query.refetch())
  const reloadInternational = useEffectEvent(() => {
    if (!query.isFetching) return query.refetch()
  })
  const ready = query.data?.geoip_available === true
  const hasInternationalActivity = useMemo(() => {
    if (activity?.scope !== scope) return false
    const confirmed = new Map(
      query.data?.points.map((point) => [point.location_id, point.last_seen])
    )
    return activity.pendingInternational.some(
      (point) =>
        point.last_seen >= (query.data?.sample_start ?? 0) &&
        point.last_seen > (confirmed.get(point.location_id) ?? 0)
    )
  }, [activity, scope, query.data])

  useEffect(() => {
    if (options.paused || !live) {
      setActivity((previous) => ({
        scope,
        status: options.paused ? 'paused' : 'history',
        pulses: [],
        pendingInternational:
          previous?.scope === scope ? previous.pendingInternational : [],
      }))
      return
    }
    if (!ready) return
    let stopped = false
    let active: AbortController | undefined
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let pulseTimer: ReturnType<typeof setTimeout> | undefined
    let retryDelay = 1000
    if (checkpoint.current?.scope !== scope) {
      checkpoint.current = {
        scope,
        cursor: latestSnapshot()?.cursor ?? '',
        seen: new Set(
          latestSnapshot()?.points.map(
            (point) => `${point.location_id}:${point.last_seen}`
          )
        ),
        pendingInternational: [],
        refreshingInternational: false,
      }
    }
    const current = checkpoint.current
    let cursor = current.cursor
    const seen = current.seen
    const publishStatus = (status: StreamStatus) =>
      setActivity((previous) => ({
        scope,
        status,
        pulses:
          status !== 'connecting' &&
          status !== 'paused' &&
          previous?.scope === scope
            ? previous.pulses
            : [],
        pendingInternational: current.pendingInternational,
      }))
    const connect = async () => {
      if (stopped || active || document.hidden) return
      const connection = new AbortController()
      active = connection
      let reset = false
      publishStatus('connecting')
      try {
        // Omit the end so this connection follows newly arriving requests.
        await streamRequestOrigins(
          {
            start_timestamp: params.start_timestamp,
            username: params.username,
          },
          isAdmin,
          cursor,
          connection.signal,
          (event) => {
            if (stopped || connection.signal.aborted) return
            if (event.event === 'reset') {
              reset = true
              connection.abort()
              return
            }
            if (event.event === 'ready') {
              retryDelay = 1000
              publishStatus('live')
              return
            }
            if (event.event !== 'origins') return
            const batch = JSON.parse(event.data) as {
              cursor?: string
              points?: unknown[]
            }
            if (
              !batch.cursor ||
              !Array.isArray(batch.points) ||
              !batch.points.every(isRequestOriginPoint)
            ) {
              reset = true
              connection.abort()
              return
            }
            cursor = batch.cursor
            current.cursor = cursor
            const pulses: RequestOriginPulse[] = []
            for (const point of batch.points) {
              const key = `${point.location_id}:${point.last_seen}`
              if (seen.has(key)) continue
              seen.add(key)
              pulses.push({ ...point, pulse_id: key })
            }
            while (seen.size > 1000) {
              const oldest = seen.values().next()
              if (oldest.done) break
              seen.delete(oldest.value)
            }
            if (!pulses.length) return
            const snapshot = latestSnapshot()
            const confirmed = new Map(
              snapshot?.points.map((point) => [
                point.location_id,
                point.last_seen,
              ])
            )
            const pending = new Map(
              current.pendingInternational
                .filter(
                  (point) =>
                    point.last_seen >= (snapshot?.sample_start ?? 0) &&
                    point.last_seen > (confirmed.get(point.location_id) ?? 0)
                )
                .map((point) => [point.location_id, point])
            )
            const hadPending = pending.size > 0
            for (const point of pulses) {
              if (
                !isDomesticRequestOrigin(point) &&
                point.last_seen >= (snapshot?.sample_start ?? 0) &&
                point.last_seen > (confirmed.get(point.location_id) ?? 0)
              ) {
                pending.set(point.location_id, point)
              }
            }
            current.pendingInternational = [...pending.values()].slice(-100)
            setActivity((previous) => ({
              scope,
              status: 'live',
              pendingInternational: current.pendingInternational,
              pulses: [
                ...(previous?.scope === scope ? previous.pulses : []),
                ...pulses,
              ].slice(-60),
            }))
            if (
              !hadPending &&
              pending.size > 0 &&
              snapshot?.points.every(isDomesticRequestOrigin) &&
              !current.refreshingInternational
            ) {
              const refresh = reloadInternational()
              if (refresh) {
                current.refreshingInternational = true
                void refresh.finally(() => {
                  current.refreshingInternational = false
                })
              }
            }
            clearTimeout(pulseTimer)
            pulseTimer = setTimeout(() => {
              if (!stopped) {
                setActivity((previous) =>
                  previous?.scope === scope
                    ? { ...previous, pulses: [] }
                    : previous
                )
              }
            }, 3000)
          }
        )
      } catch (error) {
        if (
          isAxiosError(error) &&
          [401, 403].includes(error.response?.status ?? 0)
        ) {
          stopped = true
          publishStatus('paused')
          return
        }
        if (!connection.signal.aborted) publishStatus('reconnecting')
      } finally {
        active = undefined
      }
      if (stopped || document.hidden) return
      if (reset) {
        const result = await reload()
        if (stopped) return
        cursor = result.data?.cursor ?? cursor
        current.cursor = cursor
      }
      retryTimer = setTimeout(() => void connect(), retryDelay)
      retryDelay = Math.min(retryDelay * 2, 10_000)
    }
    const onVisibility = () => {
      clearTimeout(retryTimer)
      if (document.hidden) {
        active?.abort()
        setActivity({
          scope,
          status: 'paused',
          pulses: [],
          pendingInternational: current.pendingInternational,
        })
      } else {
        void reload()
        void connect()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)
    void connect()
    return () => {
      stopped = true
      active?.abort()
      clearTimeout(retryTimer)
      clearTimeout(pulseTimer)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [scope, ready, options.paused, live, isAdmin, params])

  let status: StreamStatus =
    activity?.scope === scope ? activity.status : 'connecting'
  if (!live) status = 'history'
  if (options.paused) status = 'paused'
  return {
    query,
    status,
    pulses: !options.paused && activity?.scope === scope ? activity.pulses : [],
    params,
    isAdmin,
    live,
    hasInternationalActivity,
  }
}
