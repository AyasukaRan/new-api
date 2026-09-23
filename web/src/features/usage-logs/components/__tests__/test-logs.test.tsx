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
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { Route as LogsRoute } from '@/routes/_authenticated/usage-logs/$section'
import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema } from '../../data/schema'
import { UsageLogs } from '../../index'
import { DetailsDialog } from '../dialogs/details-dialog'

const clients: QueryClient[] = []
const testLog = usageLogSchema.parse({
  id: 81,
  user_id: 0,
  created_at: 1,
  type: 8,
  content: '模型测试',
  model_name: 'probe-model',
  channel: 4,
  channel_name: 'Test channel',
  prompt_tokens: 120,
  completion_tokens: 16,
  use_time: 2,
  other: JSON.stringify({
    model_ratio: 1,
    completion_ratio: 2,
    group_ratio: 1,
  }),
})
const usageLog = {
  ...testLog,
  id: 82,
  type: 2,
  model_name: 'real-request-model',
}

function makeClient() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  clients.push(client)
  return client
}

async function renderLogs(section: string, role = 100) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: UsageLogs,
    beforeLoad: (context) => LogsRoute.options.beforeLoad?.(context as never),
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const forbidden = createRoute({
    getParentRoute: () => root,
    path: '/403',
    component: () => <h1>Forbidden</h1>,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs]), forbidden]),
    history: createMemoryHistory({
      initialEntries: [`/usage-logs/${section}`],
    }),
  })
  render(
    <QueryClientProvider client={makeClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  let heading = section === 'test' ? 'Test Logs' : 'Common Logs'
  if (role < 10 && section === 'test') heading = 'Forbidden'
  await screen.findByRole('heading', { name: heading })
  return router
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

it('keeps the independent test page and its filters on test-only API requests', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (path) => {
    const url = new URL(path, 'http://localhost')
    if (url.pathname === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url.pathname.endsWith('/stat')) {
      return { data: { success: true, data: { quota: 0, rpm: 1, tpm: 136 } } }
    }
    return {
      data: {
        success: true,
        data: {
          items: [
            testLog,
            { ...testLog, id: 80, type: 2, model_name: 'legacy-probe-model' },
          ],
          total: 201,
        },
      },
    }
  })
  const router = await renderLogs('test')
  await screen.findByText('probe-model')
  expect(screen.getByText('legacy-probe-model')).toBeInTheDocument()
  expect(screen.getAllByText('Model test')).toHaveLength(2)
  expect(screen.queryByText('Consume')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('tab', { name: 'Only Mine' })
  ).not.toBeInTheDocument()
  expect(screen.queryByText('All Types')).not.toBeInTheDocument()
  expect(screen.getByText('Estimated test cost')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Go to next page' }))
  await waitFor(() => expect(router.state.location.search.page).toBe(2))
  expect(router.state.location.pathname).toBe('/usage-logs/test')
  await userEvent.type(screen.getByPlaceholderText('Model Name'), 'probe')
  await userEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(router.state.location.search).toMatchObject({ model: 'probe' })
  )
  expect(router.state.location.pathname).toBe('/usage-logs/test')
  await userEvent.click(screen.getByRole('button', { name: 'Reset' }))
  await waitFor(() =>
    expect(router.state.location.search.model).toBeUndefined()
  )
  expect(router.state.location.pathname).toBe('/usage-logs/test')
  const logRequests = get.mock.calls
    .map(([path]) => new URL(path, 'http://localhost'))
    .filter((url) => url.pathname.startsWith('/api/log'))
  expect(logRequests.some((url) => url.pathname === '/api/log/stat')).toBe(true)
  expect(
    logRequests.every((url) => url.searchParams.get('source') === 'test')
  ).toBe(true)
})

it('does not show usage rows while the separate test page is loading', async () => {
  let completeTests: ((value: unknown) => void) | undefined
  const pending = new Promise((resolve) => {
    completeTests = resolve
  })
  vi.spyOn(api, 'get').mockImplementation(async (path) => {
    const url = new URL(path, 'http://localhost')
    if (url.pathname === '/api/group/') {
      return { data: { success: true, data: ['default'] } }
    }
    if (url.pathname.endsWith('/stat')) {
      return { data: { success: true, data: { quota: 0, rpm: 0, tpm: 0 } } }
    }
    if (url.searchParams.get('source') === 'test') return pending
    return { data: { success: true, data: { items: [usageLog], total: 1 } } }
  })
  const router = await renderLogs('common')
  await screen.findByText('real-request-model')
  await act(() =>
    router.navigate({ to: '/usage-logs/$section', params: { section: 'test' } })
  )
  await screen.findByRole('heading', { name: 'Test Logs' })
  expect(screen.queryByText('real-request-model')).not.toBeInTheDocument()
  await act(async () => {
    completeTests?.({ data: { success: true, data: { items: [], total: 0 } } })
  })
  expect(
    await screen.findByText(
      'No test logs available. Successful model tests with usage data will appear here.'
    )
  ).toBeInTheDocument()
})

it.each([2, 8])(
  'shows test tokens, timing and billing details for test-page type %i',
  (type) => {
    render(
      <QueryClientProvider client={makeClient()}>
        <DetailsDialog
          log={{ ...testLog, type }}
          source='test'
          isAdmin
          isRoot
          open
          onOpenChange={() => undefined}
        />
      </QueryClientProvider>
    )
    expect(screen.getByText('Model test')).toBeInTheDocument()
    expect(screen.getByText('Input Tokens')).toBeInTheDocument()
    expect(screen.getByText('Output Tokens')).toBeInTheDocument()
    expect(screen.getByText('Billing Details')).toBeInTheDocument()
  }
)

it('denies direct test-page access to regular users before any test API request', async () => {
  const get = vi.spyOn(api, 'get')
  const router = await renderLogs('test', 1)
  expect(router.state.location.pathname).toBe('/403')
  expect(
    get.mock.calls.some(([path]) => String(path).includes('/api/log'))
  ).toBe(false)
})
