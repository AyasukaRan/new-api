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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatQuota, formatTimestamp } from '@/lib/format'

import { UserQuotaCell } from '../user-quota-cell'

const subscription = {
  id: 11,
  plan_id: 3,
  plan_title: 'Daily plan',
  amount_used: 250000,
  amount_total: 1000000,
  next_reset_time: 2100086400,
  last_reset_time: 2100000000,
  end_time: 2200000000,
}

it('shows subscription usage and scheduled reset even when the wallet is empty', () => {
  render(
    <UserQuotaCell used={0} remaining={0} subscriptions={[subscription]} />
  )
  expect(screen.getByText('Daily plan')).toBeVisible()
  expect(
    screen.getByText(`${formatQuota(250000)} / ${formatQuota(1000000)}`)
  ).toBeVisible()
  expect(
    screen.getByText(formatTimestamp(subscription.next_reset_time))
  ).toBeVisible()
})

it('shows unlimited usage and no automatic reset without inventing a zero limit or date', () => {
  render(
    <UserQuotaCell
      used={0}
      remaining={0}
      subscriptions={[
        {
          ...subscription,
          amount_total: 0,
          next_reset_time: 0,
        },
      ]}
    />
  )
  expect(screen.getByText(`${formatQuota(250000)} / Unlimited`)).toBeVisible()
  expect(screen.getByText('No automatic reset')).toBeVisible()
})

it('distinguishes no active subscriptions from unavailable subscription data', () => {
  const view = render(
    <UserQuotaCell used={0} remaining={0} subscriptions={[]} />
  )
  expect(screen.getByText('No active subscriptions')).toBeVisible()
  view.rerender(<UserQuotaCell used={0} remaining={0} />)
  expect(screen.getByText('Subscription usage unavailable')).toBeVisible()
  expect(screen.queryByText('No active subscriptions')).not.toBeInTheDocument()
})

it('shows two subscriptions and opens the complete management view by keyboard', async () => {
  const manage = vi.fn()
  render(
    <UserQuotaCell
      used={0}
      remaining={0}
      onManageSubscriptions={manage}
      subscriptions={[
        subscription,
        { ...subscription, id: 12, plan_title: 'Weekly plan' },
        { ...subscription, id: 13, plan_title: 'Monthly plan' },
      ]}
    />
  )
  expect(screen.getByText('Weekly plan')).toBeVisible()
  expect(screen.queryByText('Monthly plan')).not.toBeInTheDocument()
  const button = screen.getByRole('button', { name: 'Manage Subscriptions' })
  button.focus()
  await userEvent.keyboard('{Enter}')
  expect(manage).toHaveBeenCalledOnce()
  expect(screen.getByText('+1 more subscriptions')).toBeVisible()
})

it('shows wallet balance without deriving a wallet total from mixed cumulative usage', () => {
  render(<UserQuotaCell used={500000} remaining={1000000} subscriptions={[]} />)
  expect(
    screen.getByText(formatQuotaWithCurrency(1000000, { showSymbol: false }))
  ).toBeVisible()
  expect(
    screen.queryByText(formatQuotaWithCurrency(1500000, { showSymbol: false }))
  ).not.toBeInTheDocument()
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
})
