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
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { UsageLogs } from '../..'
import { usageLogSchema } from '../../data/schema'

function entry(id: number) {
  return usageLogSchema.parse({
    id,
    user_id: 1,
    created_at: 1788840000 + id,
    type: 2,
    content: `Summary for request ${id}`,
    model_name: `model-${id}`,
    request_id: `request-${id}`,
    other: JSON.stringify({ admin_info: { trace_id: `trace-${id}` } }),
  })
}

afterEach(() => {
  cleanup()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  localStorage.clear()
})

function renderLogs() {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'tester', role: ROLE.ADMIN })
  let records = [entry(11), entry(10)]
  const requestedTraces: string[] = []
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data: unknown = {}
    if (url.startsWith('/api/log/?')) {
      data = { items: records, total: records.length }
    } else if (url === '/api/log/trace') {
      const params = config?.params as { trace_id?: string } | undefined
      const trace = params?.trace_id
      if (!trace) throw new Error('Expected a trace ID')
      requestedTraces.push(trace)
      data = {
        trace_id: trace,
        legs: [
          {
            seq: 1,
            attempt: 0,
            direction: 'client_response',
            channel_id: 1,
            format: 'openai',
            method: 'POST',
            url: '',
            status: 200,
            headers: null,
            body: '',
            body_size: 0,
            truncated: false,
            has_object: false,
            rendered: { content: `Reply for ${trace}`, stream: false },
          },
        ],
      }
    } else if (
      url === '/api/subscription/admin/plans' ||
      url === '/api/group/'
    ) {
      data = []
    }
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({ getParentRoute: () => root, id: '_authenticated' })
  const logs = createRoute({
    getParentRoute: () => auth,
    path: '/usage-logs/$section',
    component: UsageLogs,
    validateSearch: (search: Record<string, unknown>) => search,
  })
  const router = createRouter({
    routeTree: root.addChildren([auth.addChildren([logs])]),
    history: createMemoryHistory({ initialEntries: ['/usage-logs/common'] }),
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return {
    requestedTraces,
    refresh: async (ids: number[]) => {
      records = ids.map(entry)
      await act(async () => {
        await client.invalidateQueries({ queryKey: ['logs'] })
      })
      await waitFor(() =>
        expect(screen.getByText(records[0].model_name)).toBeInTheDocument()
      )
    },
  }
}

test.each(['desktop', 'mobile'] as const)(
  'keeps the selected %s trace open as refreshed logs move it off the current page',
  async (layout) => {
    if (layout === 'mobile') {
      const previous = window.matchMedia
      vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
        ...previous(query),
        matches: query === '(max-width: 640px)' || previous(query).matches,
      }))
    }
    const user = userEvent.setup()
    const { refresh, requestedTraces } = renderLogs()
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 11' })
    )
    await user.click(
      await screen.findByRole('button', { name: 'View request trace' })
    )
    const trace = await screen.findByRole('dialog', { name: 'Request Trace' })
    expect(await within(trace).findByText('Reply for trace-11')).toBeVisible()
    await user.click(
      within(trace).getByRole('tab', { name: 'Exchange details' })
    )

    for (const ids of [
      [12, 11],
      [13, 12],
    ]) {
      await refresh(ids)
      expect(requestedTraces).toEqual(['trace-11'])
      const openTrace = within(
        screen.getByRole('dialog', { name: 'Request Trace' })
      )
      expect(openTrace.getByText('trace-11')).toBeVisible()
      expect(
        openTrace.getByRole('tab', { name: 'Exchange details' })
      ).toHaveAttribute('aria-selected', 'true')
    }

    await user.keyboard('{Escape}')
    const details = await screen.findByRole('dialog', { name: /Log Details/ })
    expect(within(details).getByText('request-11')).toBeVisible()
    await user.keyboard('{Escape}')
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 13' })
    )
    await user.click(
      await screen.findByRole('button', { name: 'View request trace' })
    )
    expect(await screen.findByText('Reply for trace-13')).toBeVisible()
  }
)

test('clears the selected trace and details when the signed-in account changes', async () => {
  const user = userEvent.setup()
  renderLogs()
  await user.click(
    await screen.findByRole('button', { name: 'Summary for request 11' })
  )
  await user.click(
    await screen.findByRole('button', { name: 'View request trace' })
  )
  expect(await screen.findByText('Reply for trace-11')).toBeVisible()
  await act(async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'other-admin', role: ROLE.ADMIN })
  })
  await screen.findByRole('button', { name: 'Summary for request 11' })
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(screen.queryByText('Reply for trace-11')).not.toBeInTheDocument()
})
