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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CommonLogsFilterBar } from '../common-logs-filter-bar'
import { UsageLogsProvider } from '../usage-logs-provider'

const now = new Date(2026, 10, 12, 12, 34, 56)
const midnight = new Date(2026, 10, 12).getTime()
const clients: QueryClient[] = []
const pointerCaptureDescriptor = Object.getOwnPropertyDescriptor(
  HTMLElement.prototype,
  'setPointerCapture'
)

function FilterFixture() {
  const table = useReactTable({
    data: [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <UsageLogsProvider>
      <CommonLogsFilterBar table={table} />
    </UsageLogsProvider>
  )
}

async function renderFilter(search = '') {
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: FilterFixture,
    validateSearch: (value: Record<string, unknown>) => value,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [`/usage-logs/common${search}`],
    }),
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
  await screen.findByPlaceholderText('Model Name')
  await waitFor(() => expect(client.isFetching()).toBe(0))
  return { router, client }
}

function latestStatsParams() {
  const request = vi
    .mocked(api.get)
    .mock.calls.map(([url]) => new URL(url, 'http://localhost'))
    .reverse()
    .find((url) => url.pathname === '/api/log/stat')
  expect(request).toBeDefined()
  return Object.fromEntries(request?.searchParams ?? [])
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(now)
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  Object.defineProperty(HTMLElement.prototype, 'setPointerCapture', {
    configurable: true,
    value: vi.fn(),
  })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    const path = new URL(url, 'http://localhost').pathname
    if (path === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (path === '/api/user/' || path === '/api/user/search') {
      return { data: { success: true, data: { items: [], total: 0 } } }
    }
    return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
  })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
  vi.useRealTimers()
  if (pointerCaptureDescriptor) {
    Object.defineProperty(
      HTMLElement.prototype,
      'setPointerCapture',
      pointerCaptureDescriptor
    )
  } else {
    Reflect.deleteProperty(HTMLElement.prototype, 'setPointerCapture')
  }
})

it('selects Today as a live range from local midnight through now and preserves saved filters', async () => {
  const { router } = await renderFilter(
    '?startTime=1000000&endTime=2000000&page=3&model=model-a&group=default'
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /^1970/ }))
  await user.click(screen.getByRole('button', { name: 'Today' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      timeRange: 'today',
      startTime: midnight,
      endTime: now.getTime(),
      page: 1,
      model: 'model-a',
      group: 'default',
    })
  )
  expect(
    screen.getByRole('button', { name: 'Auto refresh (30s)' })
  ).toHaveAttribute('aria-pressed', 'true')
  await user.click(screen.getByRole('button', { name: /^2026/ }))
  expect(screen.getByRole('button', { name: 'Today' })).toHaveAttribute(
    'aria-pressed',
    'true'
  )
})

it('preserves unsubmitted model and username drafts across live URL updates while statistics use saved filters', async () => {
  const { router, client } = await renderFilter(
    '?timeRange=today&model=saved-model&username=saved-user&group=default'
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /^Expand/ }))
  const model = screen.getByPlaceholderText('Model Name')
  const username = screen.getByRole('combobox', { name: 'Username' })
  await user.clear(model)
  await user.type(model, 'draft-model')
  await user.clear(username)
  await user.type(username, 'draft-user')
  await user.tab()
  const next = new Date(2026, 10, 12, 12, 35, 26)
  vi.setSystemTime(next)
  await act(() =>
    router.navigate({
      from: '/usage-logs/$section',
      to: '/usage-logs/$section',
      params: { section: 'common' },
      search: { ...router.state.location.search, endTime: next.getTime() },
      replace: true,
      resetScroll: false,
    })
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(model).toHaveValue('draft-model')
  expect(username).toHaveValue('draft-user')
  expect(router.state.location.search).toMatchObject({
    model: 'saved-model',
    username: 'saved-user',
    endTime: next.getTime(),
  })
  expect(latestStatsParams()).toMatchObject({
    model_name: 'saved-model',
    username: 'saved-user',
    group: 'default',
    start_timestamp: String(midnight / 1000),
    end_timestamp: String(next.getTime() / 1000),
  })
  expect(
    screen.getByRole('button', {
      name: '2026-11-12 00:00:00 ~ 2026-11-12 12:35:26',
    })
  ).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      model: 'draft-model',
      username: 'draft-user',
    })
  )
  await waitFor(() =>
    expect(latestStatsParams()).toMatchObject({
      model_name: 'draft-model',
      username: 'draft-user',
    })
  )
})

it('disables automatic ranges after applying manually chosen fixed dates', async () => {
  const { router } = await renderFilter('?timeRange=today&model=model-a')
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /^2026/ }))
  fireEvent.change(screen.getByLabelText('Start Time'), {
    target: { value: '2026-11-10T09:30:15' },
  })
  fireEvent.change(screen.getByLabelText('End Time'), {
    target: { value: '2026-11-11T17:45:59' },
  })
  await user.click(screen.getByRole('button', { name: 'Confirm' }))
  await user.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      startTime: new Date(2026, 10, 10, 9, 30, 15).getTime(),
      endTime: new Date(2026, 10, 11, 17, 45, 59).getTime(),
      model: 'model-a',
    })
  )
  expect(router.state.location.search).not.toHaveProperty('timeRange')
  const toggle = screen.getByRole('button', { name: 'Auto refresh (30s)' })
  expect(toggle).toBeDisabled()
  expect(toggle).toHaveAttribute('aria-pressed', 'false')
})

it('keeps saved filters and a pending model draft when automatic refresh is paused and resumed', async () => {
  const { router } = await renderFilter(
    '?timeRange=today&model=saved-model&group=default&token=main&type=%5B%222%22%5D'
  )
  const user = userEvent.setup()
  const model = screen.getByPlaceholderText('Model Name')
  await user.clear(model)
  await user.type(model, 'draft-model')
  const toggle = screen.getByRole('button', { name: 'Auto refresh (30s)' })
  await user.click(toggle)
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      autoRefresh: false,
      model: 'saved-model',
      group: 'default',
      token: 'main',
      type: ['2'],
    })
  )
  expect(toggle).toHaveAttribute('aria-pressed', 'false')
  expect(model).toHaveValue('draft-model')
  vi.setSystemTime(new Date(2026, 10, 12, 12, 36, 26))
  await user.click(toggle)
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      autoRefresh: true,
      model: 'saved-model',
      group: 'default',
      token: 'main',
      type: ['2'],
      endTime: new Date(2026, 10, 12, 12, 36, 26).getTime(),
    })
  )
  expect(toggle).toHaveAttribute('aria-pressed', 'true')
  expect(model).toHaveValue('draft-model')
})

it('advances a paused relative range to now when Search is pressed without resuming automatic refresh', async () => {
  const { router } = await renderFilter(
    `?timeRange=today&autoRefresh=false&startTime=${midnight}&endTime=${now.getTime()}&model=model-a`
  )
  const next = new Date(2026, 10, 13, 0, 1, 2)
  vi.setSystemTime(next)
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({
      timeRange: 'today',
      startTime: new Date(2026, 10, 13).getTime(),
      endTime: next.getTime(),
      model: 'model-a',
      autoRefresh: false,
    })
  )
  expect(
    screen.getByRole('button', { name: 'Auto refresh (30s)' })
  ).toHaveAttribute('aria-pressed', 'false')
})
