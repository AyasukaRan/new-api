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
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { CommonLogsStats } from '../common-logs-stats'
import { UsageLogsProvider, useUsageLogsContext } from '../usage-logs-provider'

beforeEach(() => {
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})

const clients: QueryClient[] = []
const stats = {
  quota: 500000,
  rpm: 12,
  tpm: 4000,
  finance: {
    cost_quota: 650000,
    profit_quota: -150000,
    covered_revenue_quota: 500000,
    recorded_count: 10,
    actual_count: 5,
    estimated_count: 5,
    unrecorded_count: 0,
  },
}
const response = (data = stats) => ({ data: { success: true, data } })
function Controls() {
  const { sensitiveVisible, setSensitiveVisible, viewScope, setViewScope } =
    useUsageLogsContext()
  return (
    <>
      <button
        type='button'
        onClick={() => setSensitiveVisible(!sensitiveVisible)}
      >
        Toggle amounts
      </button>
      <button
        type='button'
        onClick={() => setViewScope(viewScope === 'all' ? 'self' : 'all')}
      >
        Toggle scope
      </button>
      <CommonLogsStats />
    </>
  )
}
async function renderStats(
  options: { role?: number; source?: 'usage' | 'test'; search?: string } = {}
) {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: options.role ?? 100 })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    validateSearch: (search: Record<string, unknown>) => search,
    component: () => (
      <UsageLogsProvider source={options.source}>
        <Controls />
      </UsageLogsProvider>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({
      initialEntries: [
        `/usage-logs/${options.source === 'test' ? 'test' : 'common'}${options.search ?? ''}`,
      ],
    }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Toggle amounts' })
  return { router, client }
}
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
  vi.restoreAllMocks()
})

it('shows administrator cost and signed profit from the complete filtered statistics response', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue(response())
  await renderStats({
    search:
      '?page=4&pageSize=20&model=model-a&channel=3&username=fp&requestId=req-1&upstreamRequestId=upstream-1&type=%5B%222%22%5D&token=main&group=default&startTime=1000000&endTime=2000000',
  })
  expect(await screen.findByText('$1.3')).toBeVisible()
  expect(screen.getByText('-$0.3')).toBeVisible()
  const url = new URL(get.mock.calls[0][0], 'http://localhost')
  expect(url.pathname).toBe('/api/log/stat')
  expect(Object.fromEntries(url.searchParams)).toMatchObject({
    model_name: 'model-a',
    channel: '3',
    username: 'fp',
    request_id: 'req-1',
    upstream_request_id: 'upstream-1',
    type: '2',
    token_name: 'main',
    group: 'default',
    start_timestamp: '1000',
    end_timestamp: '2000',
  })
  expect(url.searchParams.has('p')).toBe(false)
  expect(url.searchParams.has('page_size')).toBe(false)
})

it('masks cost and profit with the existing sensitive-value toggle', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(response())
  await renderStats()
  await screen.findByText('$1.3')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle amounts' }))
  expect(screen.queryByText('$1.3')).not.toBeInTheDocument()
  expect(screen.queryByText('-$0.3')).not.toBeInTheDocument()
  expect(
    within(screen.getByRole('group', { name: 'Upstream cost' })).getByText(
      '••••'
    )
  ).toBeVisible()
  expect(
    within(screen.getByRole('group', { name: 'Gross profit' })).getByText(
      '••••'
    )
  ).toBeVisible()
})

it.each([{ role: 1 }, { role: 100, source: 'test' as const }])(
  'omits finance outside administrator usage view: %j',
  async (options) => {
    vi.spyOn(api, 'get').mockResolvedValue(response())
    await renderStats(options)
    await screen.findByText('12')
    expect(screen.queryByText('Upstream cost')).not.toBeInTheDocument()
    expect(screen.queryByText('Gross profit')).not.toBeInTheDocument()
  }
)

it('immediately hides finance when an administrator switches to personal scope or is downgraded', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(response())
  await renderStats()
  await screen.findByText('$1.3')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle scope' }))
  expect(screen.queryByText('Upstream cost')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Toggle scope' }))
  await screen.findByText('$1.3')
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 1 })
  )
  expect(screen.queryByText('Upstream cost')).not.toBeInTheDocument()
})

it.each(['filter', 'account'])(
  'does not present cached amounts as the new %s while loading or after failure',
  async (change) => {
    let finish: ((value: unknown) => void) | undefined
    const pending = new Promise((resolve) => {
      finish = resolve
    })
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce(response())
      .mockImplementation(() => pending as never)
    const { router } = await renderStats()
    await screen.findByText('$1.3')
    if (change === 'filter') {
      await act(() => router.navigate({ search: { model: 'other' } as never }))
    } else {
      act(() =>
        useAuthStore
          .getState()
          .auth.setUser({ id: 2, username: 'next-admin', role: 100 })
      )
    }
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2))
    expect(screen.queryByText('$1.3')).not.toBeInTheDocument()
    expect(screen.queryByText('$0')).not.toBeInTheDocument()
    expect(screen.getByRole('status', { name: 'Loading' })).toBeVisible()
    await act(async () =>
      finish?.({ data: { success: false, message: 'Statistics unavailable' } })
    )
    expect(await screen.findAllByText('Loading failed')).toHaveLength(2)
    expect(screen.queryByText('$1.3')).not.toBeInTheDocument()
    expect(screen.queryByText('$0')).not.toBeInTheDocument()
  }
)

it.each([
  undefined,
  {
    ...stats.finance,
    recorded_count: 0,
    unrecorded_count: 10,
    cost_quota: 0,
    profit_quota: 0,
  },
])('shows missing cost as unrecorded rather than zero', async (finance) => {
  vi.spyOn(api, 'get').mockResolvedValue(
    response({ ...stats, finance } as typeof stats)
  )
  await renderStats()
  expect(await screen.findAllByText('Not recorded')).toHaveLength(2)
  expect(screen.queryByText('$0')).not.toBeInTheDocument()
})

it('retains recorded zero cost and explains partial cost coverage only when it exists', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(
    response({
      ...stats,
      finance: {
        ...stats.finance,
        cost_quota: 0,
        profit_quota: 500000,
        unrecorded_count: 1,
      },
    })
  )
  await renderStats()
  expect(await screen.findByText('$0')).toBeVisible()
  expect(screen.queryByText('Not recorded')).not.toBeInTheDocument()
  expect(
    screen.getByText(
      'Cost and profit include only requests with recorded costs.'
    )
  ).toBeVisible()
})

it('shows zero financial totals for an explicitly empty filtered result', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(
    response({
      quota: 0,
      rpm: 0,
      tpm: 0,
      finance: {
        cost_quota: 0,
        profit_quota: 0,
        covered_revenue_quota: 0,
        recorded_count: 0,
        actual_count: 0,
        estimated_count: 0,
        unrecorded_count: 0,
      },
    })
  )
  await renderStats()
  const cost = await screen.findByRole('group', { name: 'Upstream cost' })
  expect(within(cost).getByText('$0')).toBeVisible()
  expect(
    within(screen.getByRole('group', { name: 'Gross profit' })).getByText('$0')
  ).toBeVisible()
  expect(screen.queryByText('Not recorded')).not.toBeInTheDocument()
})
