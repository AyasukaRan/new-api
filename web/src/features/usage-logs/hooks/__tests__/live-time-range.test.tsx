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
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  getRouteApi,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { getLogStats } from '../../api'
import { getLogTimePreset, getRelativeLogTimeRange } from '../../lib/time-range'
import { buildApiParams, fetchLogsByCategory } from '../../lib/utils'
import { useLiveLogRange } from '../use-live-log-range'

const route = getRouteApi('/_authenticated/usage-logs/$section')
const clients: QueryClient[] = []
const now = new Date(2026, 10, 12, 12, 0, 0)

function LiveLogsFixture() {
  const search = route.useSearch()
  useLiveLogRange(getLogTimePreset(search), search.autoRefresh !== false)
  useQuery({
    queryKey: ['logs', search],
    queryFn: () =>
      fetchLogsByCategory({
        logCategory: 'common',
        isAdmin: true,
        page: search.page ?? 1,
        pageSize: 20,
        searchParams: search,
        columnFilters: [],
      }),
  })
  useQuery({
    queryKey: ['usage-logs-stats', search],
    queryFn: () =>
      getLogStats(
        buildApiParams({
          page: 1,
          pageSize: 1,
          searchParams: search,
          isAdmin: true,
        })
      ),
  })
  return <output aria-label='Current time range'>{search.endTime}</output>
}

async function renderLiveLogs(search = '') {
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    validateSearch: (value: Record<string, unknown>) => value,
    component: LiveLogsFixture,
  })
  const history = createMemoryHistory({
    initialEntries: [`/usage-logs/common${search}`],
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history,
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByLabelText('Current time range')
  await waitFor(() => expect(client.isFetching()).toBe(0))
  return { router, client, history }
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
  vi.setSystemTime(now)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { items: [], total: 0, quota: 0, rpm: 0, tpm: 0 },
    },
  })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('recognizes relative presets while preserving explicit fixed or partial ranges', () => {
  expect(getLogTimePreset({})).toBe('today')
  expect(
    getLogTimePreset({ timeRange: '7d', startTime: 1000, endTime: 2000 })
  ).toBe('7d')
  expect(getLogTimePreset({ startTime: 0, endTime: 2000 })).toBeUndefined()
  expect(getLogTimePreset({ endTime: 2000 })).toBeUndefined()
  expect(
    getLogTimePreset({ timeRange: 'invalid', startTime: 1000 })
  ).toBeUndefined()
})

it.each([
  ['today', new Date(2026, 10, 12)],
  ['7d', new Date(2026, 10, 6)],
  ['week', new Date(2026, 10, 8)],
  ['30d', new Date(2026, 9, 14)],
  ['month', new Date(2026, 10, 1)],
] as const)(
  'resolves %s to its calendar start through the current instant',
  (preset, start) => {
    expect(getRelativeLogTimeRange(preset, now)).toEqual({ start, end: now })
  }
)

it('advances the URL snapshot and sends identical list and statistics time bounds without adding history', async () => {
  const { router, client, history } = await renderLiveLogs(
    '?timeRange=today&model=model-a&token=main&group=default'
  )
  const historyLength = history.length
  await act(() => vi.advanceTimersByTimeAsync(30_000))
  await waitFor(() => expect(client.isFetching()).toBe(0))
  const end = now.getTime() + 30_000
  expect(router.state.location.search).toMatchObject({
    timeRange: 'today',
    startTime: new Date(2026, 10, 12).getTime(),
    endTime: end,
    model: 'model-a',
    token: 'main',
    group: 'default',
  })
  const urls = vi
    .mocked(api.get)
    .mock.calls.map(([url]) => new URL(url, 'http://localhost'))
  for (const path of ['/api/log/', '/api/log/stat']) {
    const request = [...urls].reverse().find((url) => url.pathname === path)
    expect(Object.fromEntries(request?.searchParams ?? [])).toMatchObject({
      start_timestamp: String(new Date(2026, 10, 12).getTime() / 1000),
      end_timestamp: String(end / 1000),
      model_name: 'model-a',
      token_name: 'main',
      group: 'default',
    })
  }
  expect(history.length).toBe(historyLength)
  expect(window.scrollTo).not.toHaveBeenCalled()
})

it('moves today to the next local midnight when an automatic refresh crosses the date boundary', async () => {
  vi.setSystemTime(new Date(2026, 10, 12, 23, 59, 50))
  const { router } = await renderLiveLogs('?timeRange=today')
  await act(() => vi.advanceTimersByTimeAsync(30_000))
  expect(router.state.location.search).toMatchObject({
    startTime: new Date(2026, 10, 13).getTime(),
    endTime: new Date(2026, 10, 13, 0, 0, 20).getTime(),
  })
})

it.each([
  '?timeRange=today&autoRefresh=false&startTime=1000&endTime=2000',
  '?startTime=1000&endTime=2000&autoRefresh=true',
])('does not poll or move paused and fixed time ranges: %s', async (search) => {
  const { router } = await renderLiveLogs(search)
  const initial = router.state.location.search
  const requestCount = vi.mocked(api.get).mock.calls.length
  await act(() => vi.advanceTimersByTimeAsync(60_000))
  expect(router.state.location.search).toEqual(initial)
  expect(api.get).toHaveBeenCalledTimes(requestCount)
})

it('pauses while hidden and refreshes immediately on becoming visible', async () => {
  const hidden = vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
  const { router } = await renderLiveLogs(
    '?timeRange=today&startTime=1000&endTime=2000'
  )
  const requestCount = vi.mocked(api.get).mock.calls.length
  await act(() => vi.advanceTimersByTimeAsync(60_000))
  expect(router.state.location.search.endTime).toBe(2000)
  expect(api.get).toHaveBeenCalledTimes(requestCount)
  await act(async () => {
    hidden.mockReturnValue(false)
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(router.state.location.search.endTime).toBe(now.getTime() + 60_000)
})

it('preserves a later page and refreshes immediately when returning to the first page', async () => {
  const { router, client } = await renderLiveLogs(
    '?timeRange=today&page=3&startTime=1000&endTime=2000'
  )
  await act(() => vi.advanceTimersByTimeAsync(30_000))
  expect(router.state.location.search).toMatchObject({ page: 3, endTime: 2000 })
  await act(() =>
    router.navigate({
      from: '/usage-logs/$section',
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: (previous) => ({ ...previous, page: 1 }),
    })
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(router.state.location.search).toMatchObject({
    page: 1,
    endTime: now.getTime() + 30_000,
  })
})

it.each(['logs', 'usage-logs-stats'])(
  'does not start another refresh while a %s request is unfinished',
  async (query) => {
    const { router, client } = await renderLiveLogs(
      '?timeRange=today&startTime=1000&endTime=2000'
    )
    const initial = router.state.location.search.endTime
    let finish!: () => void
    const pending = client.fetchQuery({
      queryKey: [query, 'pending'],
      queryFn: () =>
        new Promise<boolean>((resolve) => {
          finish = () => resolve(true)
        }),
    })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(router.state.location.search.endTime).toBe(initial)
    await act(async () => {
      finish()
      await pending
    })
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(router.state.location.search.endTime).toBe(now.getTime() + 60_000)
  }
)

it('stops an active timer when paused and resumes with the latest saved filters', async () => {
  const { router, client } = await renderLiveLogs(
    '?timeRange=today&model=model-a'
  )
  await act(() =>
    router.navigate({
      from: '/usage-logs/$section',
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: (previous) => ({
        ...previous,
        autoRefresh: false,
        model: 'model-b',
      }),
    })
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  const endTime = router.state.location.search.endTime
  const requestCount = vi.mocked(api.get).mock.calls.length
  await act(() => vi.advanceTimersByTimeAsync(60_000))
  expect(router.state.location.search.endTime).toBe(endTime)
  expect(api.get).toHaveBeenCalledTimes(requestCount)
  await act(() =>
    router.navigate({
      from: '/usage-logs/$section',
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: (previous) => ({ ...previous, autoRefresh: true }),
    })
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(router.state.location.search).toMatchObject({
    autoRefresh: true,
    model: 'model-b',
    endTime: now.getTime() + 60_000,
  })
})
