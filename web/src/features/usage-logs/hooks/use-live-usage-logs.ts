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
import { isAxiosError } from 'axios'
import { useEffect, useEffectEvent, useRef, useState } from 'react'

import { streamUsageLogs } from '../api'
import type { UsageLog } from '../data/schema'
import { getRelativeLogTimeRange, type LogTimePreset } from '../lib/time-range'
import type { LiveLogSnapshot } from '../types'

interface LiveLogView {
  snapshot: LiveLogSnapshot
  items: UsageLog[]
  committedItems: UsageLog[]
  total: number
  pending: number
  reset: boolean
  pendingUncertain: boolean
  exactTotal?: (total: number) => void
}

function requestIdentity(log: UsageLog) {
  return `${log.user_id}:${log.request_id}`
}

export function useLiveUsageLogs({
  snapshot,
  preset,
  isAdmin,
  pageSize,
  isReading,
  refetch,
}: {
  snapshot?: LiveLogSnapshot
  preset?: LogTimePreset
  isAdmin: boolean
  pageSize: number
  isReading: () => boolean
  refetch: () => Promise<unknown>
}) {
  const [view, setView] = useState<LiveLogView>()
  const apply = useRef<() => void>(undefined)
  const reading = useEffectEvent(isReading)
  const reload = useEffectEvent(refetch)
  useEffect(() => {
    if (!snapshot || !preset) return
    let items = snapshot.items
    let activeItems: UsageLog[] = []
    // The active registry and committed journal arrive independently. Keep a
    // retiring projection briefly so an out-of-order completion cannot blink.
    const completing = new Map<string, UsageLog>()
    let completionTimer: ReturnType<typeof setTimeout> | undefined
    let activeSignature: string | undefined
    let total = snapshot.total
    let pending = 0
    let reset = false
    let firstBatch = true
    let pendingUncertain = false
    let needsExactTotal = false
    let totalGeneration = 0
    let cursor = snapshot.cursor
    let stopped = false
    let retryDelay = 1000
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    let active: AbortController | undefined
    const seen = new Set(items.map((log) => log.live_key))
    // Keep an immutable view behind the reader. Only the newest page needs buffering.
    let visibleItems = items
    let visibleActiveItems = activeItems
    let visibleTotal = total
    const updateTotal = (count: number, generation: number) => {
      // A stats fetch for an earlier filter or account can finish after navigation.
      if (
        stopped ||
        reset ||
        generation !== totalGeneration ||
        !needsExactTotal ||
        !Number.isSafeInteger(count) ||
        count < 0
      ) {
        return
      }
      total = count
      if (!pending && !reading()) {
        visibleTotal = total
        publish()
      }
    }
    const publish = () => {
      const generation = totalGeneration
      const activeRequests = new Set(visibleActiveItems.map(requestIdentity))
      setView({
        snapshot,
        // Active rows replace intermediate retry failures. Retain the full
        // committed page so loading older rows keeps its original page tail.
        items: [
          ...visibleActiveItems,
          ...visibleItems.filter(
            (log) => !activeRequests.has(requestIdentity(log))
          ),
        ],
        committedItems: visibleItems,
        total: visibleTotal,
        pending,
        reset,
        pendingUncertain,
        exactTotal: needsExactTotal
          ? (count) => updateTotal(count, generation)
          : undefined,
      })
    }
    const show = () => {
      pending = 0
      pendingUncertain = false
      if (reset) {
        // Keeping the reset action visible also permits retry after a failed snapshot.
        void reload()
        return
      }
      visibleItems = items
      visibleActiveItems = [...activeItems, ...completing.values()]
      visibleTotal = total
      publish()
    }
    apply.current = show
    const requireSnapshot = () => {
      if (reset || stopped) return
      reset = true
      active?.abort()
      publish()
      if (!pending && !reading() && !document.hidden) void reload()
    }
    const checkCompletions = () => {
      if (!completing.size) {
        clearTimeout(completionTimer)
        completionTimer = undefined
      } else if (completionTimer === undefined) {
        // Missing events, disabled logging, or an expired worker lease need an
        // authoritative refresh, not an indefinitely retained processing row.
        completionTimer = setTimeout(requireSnapshot, 3000)
      }
    }
    const connect = async () => {
      if (stopped || document.hidden || reset || active) return
      if (
        Math.floor(getRelativeLogTimeRange(preset).start.getTime() / 1000) !==
        snapshot.filters.start_timestamp
      ) {
        requireSnapshot()
        return
      }
      const connection = new AbortController()
      active = connection
      try {
        await streamUsageLogs(
          snapshot.filters,
          isAdmin,
          cursor,
          connection.signal,
          (event) => {
            if (connection.signal.aborted || stopped) return
            if (event.event === 'reset') {
              requireSnapshot()
              return
            }
            if (event.event === 'ready') {
              retryDelay = 1000
              return
            }
            if (event.event === 'active') {
              const batch = JSON.parse(event.data) as { items: UsageLog[] }
              if (
                !Array.isArray(batch.items) ||
                batch.items.some(
                  (log) => !log.in_progress || !log.live_key || !log.request_id
                )
              ) {
                requireSnapshot()
                return
              }
              const signature = JSON.stringify(batch.items)
              if (signature === activeSignature) return
              const changed =
                activeSignature !== undefined || batch.items.length > 0
              activeSignature = signature
              const current = new Set(batch.items.map(requestIdentity))
              const committed = new Set(items.map(requestIdentity))
              for (const log of activeItems) {
                const key = requestIdentity(log)
                if (!current.has(key) && !committed.has(key)) {
                  completing.set(key, log)
                }
              }
              for (const key of current) completing.delete(key)
              activeItems = batch.items
              checkCompletions()
              if (pending || reading()) {
                if (changed) {
                  pending = Math.max(1, pending)
                  pendingUncertain = true
                }
                publish()
              } else {
                show()
              }
              return
            }
            if (event.event !== 'logs') return
            const batch = JSON.parse(event.data) as {
              cursor: string
              items: UsageLog[]
              omitted?: number
              replace?: boolean
              total?: number
            }
            if (
              !batch.cursor ||
              !Array.isArray(batch.items) ||
              batch.items.some((log) => !log.live_key)
            ) {
              requireSnapshot()
              return
            }
            if (batch.cursor === cursor) return
            let completed = false
            for (const log of batch.items) {
              completed = completing.delete(requestIdentity(log)) || completed
            }
            checkCompletions()
            if (batch.replace === true) {
              const replacementTotal = batch.total
              if (
                typeof replacementTotal !== 'number' ||
                !Number.isSafeInteger(replacementTotal) ||
                replacementTotal < 0
              ) {
                requireSnapshot()
                return
              }
              cursor = batch.cursor
              retryDelay = 1000
              firstBatch = false
              needsExactTotal = false
              totalGeneration++
              items = batch.items.slice(0, pageSize)
              total = replacementTotal
              seen.clear()
              for (const log of items) seen.add(log.live_key)
              if (pending || reading()) {
                // A recovered request can disappear from a failure filter;
                // retain the reader's view until they accept the replacement.
                pending = Math.max(1, pending)
                pendingUncertain = true
                publish()
              } else {
                show()
              }
              return
            }
            const incoming = batch.items.filter((log) => {
              if (seen.has(log.live_key)) return false
              seen.add(log.live_key)
              return true
            })
            // Cursors handle replay across batches. Keep a bounded overlap window
            // for rows also present in the snapshot and duplicate batch deliveries.
            if (seen.size > 2 * pageSize + batch.items.length) {
              for (const key of seen) {
                seen.delete(key)
                if (seen.size <= pageSize + batch.items.length) break
              }
            }
            cursor = batch.cursor
            retryDelay = 1000
            if (!batch.items.length) return
            const omitted = batch.omitted ?? 0
            const overlaps = incoming.length < batch.items.length
            const oldestSnapshot = snapshot.items.at(-1)
            const outsideSnapshot =
              firstBatch &&
              snapshot.items.length >= pageSize &&
              oldestSnapshot &&
              incoming.some(
                (log) => log.created_at <= oldestSnapshot.created_at
              )
            const historicalStart = firstBatch && (snapshot.filters.p ?? 1) > 1
            const uncertainBatch =
              historicalStart || (omitted > 0 && (firstBatch || overlaps))
            firstBatch = false
            // Count and page reads can straddle the same commits. A truncated
            // first batch also hides its overlap beyond the snapshot page.
            // Once ambiguous, use absolute counts from the ordinary stats clock.
            if (uncertainBatch || overlaps || outsideSnapshot) {
              needsExactTotal = true
            }
            const added = incoming.length + omitted
            if (!added) {
              if (completed && !pending && !reading()) show()
              else if (needsExactTotal) publish()
              return
            }
            // Each batch already has the database's tie order. ClickHouse and
            // personal lists assign display-only IDs which cannot sort rows.
            items = [...incoming, ...items]
              .sort((a, b) => b.created_at - a.created_at)
              .slice(0, pageSize)
            if (!needsExactTotal) total += added
            if (pending || reading()) {
              pending += added
              pendingUncertain ||= uncertainBatch
              publish()
            } else {
              show()
            }
          }
        )
      } catch (error) {
        if (
          isAxiosError(error) &&
          [401, 403].includes(error.response?.status ?? 0)
        ) {
          return
        }
      } finally {
        if (active === connection) active = undefined
      }
      if (stopped || reset || connection.signal.aborted || document.hidden) {
        return
      }
      retryTimer = setTimeout(connect, retryDelay)
      retryDelay = Math.min(30_000, retryDelay * 2)
    }
    const checkVisibility = () => {
      clearTimeout(retryTimer)
      active?.abort()
      active = undefined
      if (document.hidden) return
      if (reset && !pending && !reading()) {
        void reload()
      } else {
        void connect()
      }
    }
    // Calendar boundaries change even if no new log arrives.
    let calendarTimer: ReturnType<typeof setTimeout>
    const checkRange = () => {
      if (
        Math.floor(getRelativeLogTimeRange(preset).start.getTime() / 1000) !==
        snapshot.filters.start_timestamp
      ) {
        requireSnapshot()
        return
      }
      const midnight = new Date()
      midnight.setHours(24, 0, 0, 0)
      calendarTimer = setTimeout(
        checkRange,
        Math.max(1, midnight.getTime() - Date.now())
      )
    }
    checkRange()
    document.addEventListener('visibilitychange', checkVisibility)
    void connect()
    return () => {
      stopped = true
      active?.abort()
      clearTimeout(retryTimer)
      clearTimeout(calendarTimer)
      clearTimeout(completionTimer)
      document.removeEventListener('visibilitychange', checkVisibility)
      apply.current = undefined
    }
  }, [snapshot, preset, pageSize, isAdmin])
  return {
    view: view?.snapshot === snapshot ? view : undefined,
    showLatest: () => apply.current?.(),
  }
}
