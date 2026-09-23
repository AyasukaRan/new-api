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
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { formatQuota, formatTimestamp } from '@/lib/format'

import type { PlanRecord } from '../../../types'
import {
  SubscriptionsProvider,
  useSubscriptions,
} from '../../subscriptions-provider'
import { ResetSubscriptionsDialog } from '../reset-subscriptions-dialog'
import { UserSubscriptionsDialog } from '../user-subscriptions-dialog'

const subscription = {
  id: 11,
  user_id: 7,
  plan_id: 3,
  status: 'active',
  start_time: 2000000000,
  end_time: 2200000000,
  amount_total: 1000000,
  amount_used: 250000,
  next_reset_time: 2100086400,
}
const plan: PlanRecord = {
  plan: {
    id: 3,
    title: 'Daily plan',
    price_amount: 1,
    currency: 'USD',
    duration_unit: 'month',
    duration_value: 1,
    quota_reset_period: 'daily',
    enabled: true,
    sort_order: 0,
    allow_balance_pay: true,
    allow_wallet_overflow: true,
    max_purchase_per_user: 0,
    total_amount: 1000000,
  },
}

function renderDialog(onSuccess = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  function Dialog() {
    const [open, setOpen] = useState(true)
    return (
      <UserSubscriptionsDialog
        open={open}
        onOpenChange={setOpen}
        user={{ id: 7, username: 'user-a' }}
        onSuccess={onSuccess}
      />
    )
  }
  return render(
    <QueryClientProvider client={client}>
      <Dialog />
    </QueryClientProvider>
  )
}

function mockData() {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/subscription/admin/plans' ? [plan] : [{ subscription }],
    },
  }))
}

async function openReset() {
  await screen.findByText('Daily plan')
  await userEvent.click(screen.getByRole('button', { name: 'Actions' }))
  await userEvent.click(
    await screen.findByRole('menuitem', { name: 'Reset usage' })
  )
  return screen.getByRole('alertdialog', { name: 'Reset subscription usage' })
}

afterEach(() => {
  vi.restoreAllMocks()
})

it('clears subscription usage while showing the unchanged scheduled reset time', async () => {
  let reset = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/subscription/admin/plans'
          ? [plan]
          : [
              {
                subscription: {
                  ...subscription,
                  amount_used: reset ? 0 : subscription.amount_used,
                },
              },
            ],
    },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async () => {
    reset = true
    return { data: { success: true, data: { reset_count: 1 } } }
  })
  const success = vi.fn()
  renderDialog(success)
  expect(
    await screen.findByText(formatTimestamp(subscription.next_reset_time))
  ).toBeVisible()
  const confirmation = await openReset()
  expect(
    within(confirmation).getByText(
      'Only usage is cleared. Scheduled reset times stay unchanged.'
    )
  ).toBeVisible()
  expect(within(confirmation).queryByRole('switch')).not.toBeInTheDocument()
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Reset usage' })
  )
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/subscription/admin/users/7/subscriptions/reset',
      { plan_id: 3, advance_reset_time: false }
    )
  )
  expect(
    await screen.findByText(`${formatQuota(0)} / ${formatQuota(1000000)}`)
  ).toBeVisible()
  expect(
    screen.getByText(formatTimestamp(subscription.next_reset_time))
  ).toBeVisible()
  expect(success).toHaveBeenCalledOnce()
})

it('keeps the confirmation open after a failed reset and allows retry', async () => {
  mockData()
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { success: false } })
    .mockResolvedValue({ data: { success: true, data: { reset_count: 1 } } })
  const error = vi.spyOn(toast, 'error')
  const success = vi.fn()
  renderDialog(success)
  const confirmation = await openReset()
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Reset usage' })
  )
  await waitFor(() => expect(error).toHaveBeenCalledWith('Operation failed'))
  expect(
    screen.getByRole('alertdialog', { name: 'Reset subscription usage' })
  ).toBeVisible()
  expect(success).not.toHaveBeenCalled()
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Reset usage' })
  )
  await waitFor(() => expect(success).toHaveBeenCalledOnce())
  expect(post).toHaveBeenLastCalledWith(
    '/api/subscription/admin/users/7/subscriptions/reset',
    { plan_id: 3, advance_reset_time: false }
  )
})

it('shows a retryable loading error instead of an empty subscription list', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: false } })
  renderDialog()
  expect(await screen.findByRole('button', { name: 'Retry' })).toBeVisible()
  expect(screen.getByText('Loading failed')).toBeVisible()
  expect(screen.queryByText('No subscription records')).not.toBeInTheDocument()
})

it('keeps user B subscriptions when user A responds after switching users', async () => {
  let finishA!: (result: { data: unknown }) => void
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/subscription/admin/plans') {
      return {
        data: {
          success: true,
          data: [plan, { plan: { ...plan.plan, id: 4, title: 'Weekly plan' } }],
        },
      }
    }
    if (url.includes('/users/7/')) {
      return new Promise((resolve) => {
        finishA = resolve
      })
    }
    return {
      data: {
        success: true,
        data: [{ subscription: { ...subscription, plan_id: 4, id: 12 } }],
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog
        open
        user={{ id: 7 }}
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
  await waitFor(() => expect(finishA).toBeDefined())
  view.rerender(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog
        open
        user={{ id: 8 }}
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Weekly plan')).toBeVisible()
  await act(async () => {
    finishA({ data: { success: true, data: [{ subscription }] } })
  })
  expect(screen.getByText('Weekly plan')).toBeVisible()
  expect(screen.queryByText('Daily plan')).not.toBeInTheDocument()
})

it('does not fetch while closed and clears an unconfirmed reset before reopening', async () => {
  const get = mockData()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const props = { user: { id: 7 }, onOpenChange: () => undefined }
  const view = render(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog {...props} open={false} />
    </QueryClientProvider>
  )
  expect(get).not.toHaveBeenCalled()
  view.rerender(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog {...props} open />
    </QueryClientProvider>
  )
  await openReset()
  view.rerender(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog {...props} open={false} />
    </QueryClientProvider>
  )
  view.rerender(
    <QueryClientProvider client={client}>
      <UserSubscriptionsDialog {...props} open />
    </QueryClientProvider>
  )
  await screen.findByText('Daily plan')
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
})

it('resets every active subscription in a plan without advancing the reset schedule', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: [] } })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { reset_count: 2 } } })
  function PlanReset() {
    const { setOpen, setCurrentRow } = useSubscriptions()
    return (
      <>
        <button
          type='button'
          onClick={() => {
            setCurrentRow(plan)
            setOpen('reset-subscriptions')
          }}
        >
          Reset plan
        </button>
        <ResetSubscriptionsDialog />
      </>
    )
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <SubscriptionsProvider>
        <PlanReset />
      </SubscriptionsProvider>
    </QueryClientProvider>
  )
  await userEvent.click(screen.getByRole('button', { name: 'Reset plan' }))
  expect(screen.queryByRole('switch')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Reset usage' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/subscription/admin/plans/3/subscriptions/reset',
      { advance_reset_time: false }
    )
  )
})

it('disables confirmation while a reset is pending and prevents duplicate submissions', async () => {
  mockData()
  let complete!: (result: { data: unknown }) => void
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve
      })
  )
  renderDialog()
  const confirmation = await openReset()
  const button = within(confirmation).getByRole('button', {
    name: 'Reset usage',
  })
  await userEvent.click(button)
  await waitFor(() => expect(button).toBeDisabled())
  expect(
    within(confirmation).getByRole('button', { name: 'Cancel' })
  ).toBeDisabled()
  await userEvent.click(button)
  expect(post).toHaveBeenCalledOnce()
  await act(async () => {
    complete({ data: { success: true, data: { reset_count: 1 } } })
  })
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
})
