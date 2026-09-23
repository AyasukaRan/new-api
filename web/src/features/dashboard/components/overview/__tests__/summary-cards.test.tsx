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

import { formatTimestamp } from '@/features/subscriptions/lib/format'
import type { UserSubscription } from '@/features/subscriptions/types'
import { api } from '@/lib/api'
import { formatQuota } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { SummaryCards } from '../summary-cards'

let client: QueryClient
const now = Math.floor(Date.now() / 1000)
const subscription: UserSubscription = {
  id: 1,
  user_id: 1,
  plan_id: 1,
  status: 'active',
  start_time: now - 3600,
  end_time: now + 864000,
  amount_total: 1000000,
  amount_used: 250000,
  next_reset_time: now + 86400,
}

beforeEach(() => {
  localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'member', role: 1, quota: 0 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['status'], {}, { updatedAt: Date.now() + 60000 })
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
})

function mockSubscriptions(subscriptions: UserSubscription[]) {
  return vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data:
        path === '/api/subscription/self'
          ? {
              billing_preference: 'wallet_only',
              subscriptions: subscriptions.map((subscription) => ({
                subscription,
              })),
              all_subscriptions: [],
            }
          : [],
    },
  }))
}
async function renderSummary() {
  const router = createRouter({
    routeTree: createRootRoute({ component: SummaryCards }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

it('shows an active subscription instead of reporting an empty wallet as depleted', async () => {
  mockSubscriptions([subscription])
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(await within(card).findByText('Subscription remaining')).toBeVisible()
  expect(within(card).getByText(formatQuota(750000))).toBeVisible()
  expect(within(card).getByText('Current period usage')).toBeVisible()
  expect(within(card).getByText(formatQuota(250000))).toBeVisible()
  expect(within(card).getByText('Next subscription refresh')).toBeVisible()
  expect(within(card).getByText(formatTimestamp(now + 86400))).toBeVisible()
  expect(within(card).queryByText('Balance depleted')).not.toBeInTheDocument()
  expect(within(card).queryByText('Runway')).not.toBeInTheDocument()
})

it('clamps each active subscription before summing and selects its next scheduled refresh', async () => {
  mockSubscriptions([
    { ...subscription, amount_used: 1200000 },
    {
      ...subscription,
      id: 2,
      amount_total: 500000,
      amount_used: 50000,
      next_reset_time: now + 3600,
    },
    { ...subscription, id: 3, end_time: now - 1, amount_used: 0 },
    { ...subscription, id: 4, status: 'cancelled', amount_used: 0 },
  ])
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(await within(card).findByText(formatQuota(450000))).toBeVisible()
  expect(within(card).getByText(formatTimestamp(now + 3600))).toBeVisible()
})

it('keeps an exhausted active subscription and its refresh time instead of falling back to the wallet', async () => {
  mockSubscriptions([{ ...subscription, amount_used: 1000000 }])
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(
    await within(card).findByText('Subscription quota depleted')
  ).toBeVisible()
  expect(within(card).getByText('Subscription remaining')).toBeVisible()
  expect(within(card).getByText(formatTimestamp(now + 86400))).toBeVisible()
  expect(within(card).queryByText('Balance depleted')).not.toBeInTheDocument()
})

it('preserves unlimited subscription quota and the absence of a periodic reset', async () => {
  mockSubscriptions([{ ...subscription, amount_total: 0, next_reset_time: 0 }])
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(await within(card).findByText('Unlimited')).toBeVisible()
  expect(within(card).getByText('No Reset')).toBeVisible()
  expect(
    within(card).queryByText('Subscription quota depleted')
  ).not.toBeInTheDocument()
})

it.each([
  { subscriptions: [] },
  { subscriptions: [{ ...subscription, end_time: now - 1 }] },
  { subscriptions: [{ ...subscription, status: 'cancelled' }] },
])(
  'falls back to the wallet when there are no active subscriptions: %j',
  async ({ subscriptions }) => {
    mockSubscriptions(subscriptions)
    await renderSummary()
    const card = await screen.findByRole('group', { name: 'Credit remaining' })
    expect(await within(card).findByText('Runway')).toBeVisible()
    expect(within(card).getAllByText('Balance depleted')).toHaveLength(2)
    expect(
      within(card).queryByText('Subscription remaining')
    ).not.toBeInTheDocument()
  }
)

it('does not report wallet depletion before the subscription query resolves', async () => {
  let complete: (value: unknown) => void = () => undefined
  const pending = new Promise((resolve) => {
    complete = resolve
  })
  vi.spyOn(api, 'get').mockImplementation((path) =>
    path === '/api/subscription/self'
      ? (pending as never)
      : Promise.resolve({ data: { success: true, data: [] } })
  )
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(within(card).getByText('Loading...')).toBeVisible()
  expect(within(card).queryByText('Balance depleted')).not.toBeInTheDocument()
  await act(() =>
    complete({
      data: {
        success: true,
        data: { subscriptions: [{ subscription }], all_subscriptions: [] },
      },
    })
  )
  expect(await within(card).findByText('Subscription remaining')).toBeVisible()
})

it.each([{ success: false, data: null }, { success: true }])(
  'shows a retryable error for an unsuccessful or incomplete response: %j',
  async (response) => {
    vi.spyOn(api, 'get').mockImplementation(async (path) => ({
      data:
        path === '/api/subscription/self'
          ? response
          : { success: true, data: [] },
    }))
    await renderSummary()
    const card = await screen.findByRole('group', { name: 'Credit remaining' })
    expect(
      await within(card).findByRole('button', { name: 'Retry' })
    ).toBeVisible()
    expect(within(card).queryByText('Balance depleted')).not.toBeInTheDocument()
    expect(within(card).queryByText('Runway')).not.toBeInTheDocument()
  }
)

it('removes the previous account subscription while the next account is loading', async () => {
  const get = mockSubscriptions([subscription])
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(await within(card).findByText(formatQuota(750000))).toBeVisible()

  let complete: (value: unknown) => void = () => undefined
  const pending = new Promise((resolve) => {
    complete = resolve
  })
  get.mockImplementation((path) =>
    path === '/api/subscription/self'
      ? (pending as never)
      : Promise.resolve({ data: { success: true, data: [] } })
  )
  await act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'second-member', role: 1, quota: 0 })
  )
  expect(await within(card).findByText('Loading...')).toBeVisible()
  expect(within(card).queryByText(formatQuota(750000))).not.toBeInTheDocument()
  await waitFor(() => {
    expect(
      client.getQueryData(['dashboard', 'self-subscriptions', 1])
    ).toBeUndefined()
  })
  await act(() =>
    complete({
      data: {
        success: true,
        data: { subscriptions: [], all_subscriptions: [] },
      },
    })
  )
  expect(await within(card).findByText('Runway')).toBeVisible()
  expect(within(card).getAllByText('Balance depleted')).toHaveLength(2)
})

it('offers a retry when subscription loading fails without claiming the wallet is depleted', async () => {
  let failed = true
  vi.spyOn(api, 'get').mockImplementation(async (path) => {
    if (path !== '/api/subscription/self') {
      return { data: { success: true, data: [] } }
    }
    if (failed) throw new Error('offline')
    return {
      data: {
        success: true,
        data: { subscriptions: [{ subscription }], all_subscriptions: [] },
      },
    }
  })
  const user = userEvent.setup()
  await renderSummary()
  const card = await screen.findByRole('group', { name: 'Credit remaining' })
  expect(
    await within(card).findByRole('button', { name: 'Retry' })
  ).toBeVisible()
  expect(within(card).queryByText('Balance depleted')).not.toBeInTheDocument()
  failed = false
  await user.click(within(card).getByRole('button', { name: 'Retry' }))
  expect(await within(card).findByText('Subscription remaining')).toBeVisible()
})
