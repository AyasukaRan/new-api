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
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = {}
    if (url.startsWith('/api/log/?')) {
      data = { items: records, total: records.length }
    } else if (
      url === '/api/subscription/admin/plans' ||
      url === '/api/group/'
    ) {
      data = []
    }
    return { data: { success: true, data } }
  })
  const root = createRootRoute()
  const auth = createRoute({
    getParentRoute: () => root,
    id: '_authenticated',
  })
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
  'keeps the selected %s log open as refreshed logs move it off the current page',
  async (layout) => {
    if (layout === 'mobile') {
      const previous = window.matchMedia
      vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
        ...previous(query),
        matches: query === '(max-width: 640px)' || previous(query).matches,
      }))
    }
    const user = userEvent.setup()
    const { refresh } = renderLogs()
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 11' })
    )
    for (const ids of [
      [12, 11],
      [13, 12],
    ]) {
      await refresh(ids)
      const details = within(
        await screen.findByRole('dialog', { name: /Log Details/ })
      )
      expect(details.getByText('request-11')).toBeVisible()
      expect(details.queryByText('View request trace')).not.toBeInTheDocument()
    }
    await user.keyboard('{Escape}')
    await user.click(
      await screen.findByRole('button', { name: 'Summary for request 13' })
    )
    expect(await screen.findByText('request-13')).toBeVisible()
  }
)

test('clears the selected details when the signed-in account changes', async () => {
  const user = userEvent.setup()
  renderLogs()
  await user.click(
    await screen.findByRole('button', { name: 'Summary for request 11' })
  )
  expect(await screen.findByText('request-11')).toBeVisible()
  await act(async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'other-admin', role: ROLE.ADMIN })
  })
  await screen.findByRole('button', { name: 'Summary for request 11' })
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})
