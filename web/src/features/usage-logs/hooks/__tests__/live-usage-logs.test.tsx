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
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { usageLogSchema } from '../../data/schema'
import { getRelativeLogTimeRange } from '../../lib/time-range'
import type { LiveLogSnapshot } from '../../types'
import { useLiveUsageLogs } from '../use-live-usage-logs'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function entry(id: number, userID = 1) {
  return usageLogSchema.parse({
    id,
    live_key: `log-${id}-${userID}`,
    user_id: userID,
    created_at: Math.floor(Date.now() / 1000) + id,
    type: 2,
    content: '',
    model_name: `model-${id}`,
    request_id: `request-${id}`,
  })
}

function startLiveView() {
  let reading = false
  const refetch = vi.fn()
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: new ReadableStream({
      start(controller) {
        stream = controller
      },
    }),
  }))
  const snapshot: LiveLogSnapshot = {
    cursor: 'initial',
    filters: {
      start_timestamp: Math.floor(
        getRelativeLogTimeRange('today').start.getTime() / 1000
      ),
    },
    items: [entry(1)],
    total: 1,
  }
  const hook = renderHook(
    ({ current }) =>
      useLiveUsageLogs({
        snapshot: current,
        preset: 'today',
        isAdmin: true,
        pageSize: 1,
        isReading: () => reading,
        refetch,
      }),
    { initialProps: { current: snapshot } }
  )
  return {
    ...hook,
    snapshot,
    refetch,
    read: (value: boolean) => {
      reading = value
    },
    emit: async (event: string, data: unknown) => {
      if (!stream) await waitFor(() => expect(stream).toBeDefined())
      await act(async () => {
        stream?.enqueue(
          new TextEncoder().encode(
            `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
          )
        )
      })
    },
  }
}

test('shows an active request before settlement without changing the completed total or page tail', async () => {
  const live = startLiveView()
  await live.emit('active', {
    items: [{ ...entry(2), live_key: 'active-2', in_progress: true }],
  })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'active-2',
    'log-1-1',
  ])
  expect(live.result.current.view?.total).toBe(1)
  expect(live.result.current.view?.committedItems.map((row) => row.id)).toEqual(
    [1]
  )

  await live.emit('logs', { items: [entry(2)], cursor: 'settled-2' })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'active-2',
  ])
  expect(live.result.current.view?.total).toBe(2)
  await live.emit('active', { items: [] })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'log-2-1',
  ])
})

test('keeps a completed request visible until its delayed committed event replaces the processing row', async () => {
  const live = startLiveView()
  await live.emit('active', {
    items: [{ ...entry(2), live_key: 'active-2', in_progress: true }],
  })
  await live.emit('active', { items: [] })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'active-2',
    'log-1-1',
  ])
  expect(live.result.current.view?.total).toBe(1)

  await live.emit('logs', { items: [entry(2)], cursor: 'settled-2' })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'log-2-1',
  ])
  expect(live.result.current.view?.total).toBe(2)
  expect(live.refetch).not.toHaveBeenCalled()
})

test('recovers an unmatched completion from a snapshot without interrupting an open detail', async () => {
  const live = startLiveView()
  await live.emit('active', {
    items: [{ ...entry(2), live_key: 'active-2', in_progress: true }],
  })
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  await live.emit('active', { items: [] })
  live.read(true)
  await act(async () => vi.advanceTimersByTimeAsync(5000))
  expect(live.result.current.view?.items[0].live_key).toBe('active-2')
  expect(live.result.current.view?.reset).toBe(true)
  expect(live.refetch).not.toHaveBeenCalled()

  live.read(false)
  act(() => live.result.current.showLatest())
  expect(live.refetch).toHaveBeenCalledOnce()
})

test('keeps active and settled updates behind an open detail or historical view until accepted', async () => {
  const live = startLiveView()
  live.read(true)
  await live.emit('active', {
    items: [{ ...entry(2), live_key: 'active-2', in_progress: true }],
  })
  expect(live.result.current.view?.items.map((row) => row.id)).toEqual([1])
  expect(live.result.current.view?.pending).toBeGreaterThan(0)
  await live.emit('logs', { items: [entry(2)], cursor: 'settled-2' })
  await live.emit('active', { items: [] })
  expect(live.result.current.view?.items.map((row) => row.id)).toEqual([1])
  live.read(false)
  act(() => live.result.current.showLatest())
  expect(live.result.current.view?.items.map((row) => row.id)).toEqual([2])
  expect(live.result.current.view?.total).toBe(2)
})

test('keeps a retrying request active without duplicating its committed attempt or hiding another user', async () => {
  const live = startLiveView()
  await live.emit('active', {
    items: [
      { ...entry(1), live_key: 'active-1', in_progress: true },
      { ...entry(1, 2), live_key: 'active-user-2', in_progress: true },
    ],
  })
  expect(live.result.current.view?.items.map((row) => row.live_key)).toEqual([
    'active-1',
    'active-user-2',
  ])
  expect(live.result.current.view?.total).toBe(1)
})

test('clears active rows when switching filters or user scope', async () => {
  const live = startLiveView()
  await live.emit('active', {
    items: [{ ...entry(2), live_key: 'active-2', in_progress: true }],
  })
  expect(live.result.current.view?.items[0].live_key).toBe('active-2')
  live.rerender({
    current: { ...live.snapshot, cursor: 'other-scope', items: [entry(3)] },
  })
  expect(live.result.current.view).toBeUndefined()
  await live.emit('active', { items: [] })
  expect(live.result.current.view?.items.map((row) => row.id)).toEqual([3])
})
