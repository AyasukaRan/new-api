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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import type React from 'react'
import {
  afterEach,
  beforeAll,
  beforeEach,
  describe,
  expect,
  test,
} from 'vitest'

import { useSystemConfigStore } from '@/stores/system-config-store'

import { LogCostDisplay } from '../log-cost-display'

function renderCost(
  props: React.ComponentProps<typeof LogCostDisplay>
): ReturnType<typeof render> {
  return render(<LogCostDisplay {...props} />)
}

describe('log cost display', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      Subscription: 'Subscription',
      Wallet: 'Wallet',
      'Includes tool-call surcharge': 'Includes tool-call surcharge',
    })
  })

  beforeEach(() => {
    useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  })

  afterEach(() => {
    useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
    localStorage.clear()
  })

  test.each([
    { consumed: 12500, expected: '$0.025' },
    { consumed: 0, expected: '$0' },
    { consumed: 1, expected: '$0.000002' },
    { consumed: undefined, expected: '$0.01' },
  ])(
    'shows subscription deduction $consumed without hover, falling back only when absent',
    ({ consumed, expected }) => {
      renderCost({
        quota: 5000,
        other: {
          billing_source: 'subscription',
          subscription_consumed: consumed,
        },
        showBillingSource: true,
      })

      expect(screen.getByText(expected)).toBeVisible()
      expect(screen.getByRole('img', { name: 'Subscription' })).toBeVisible()
      expect(screen.queryByText('Subscription')).not.toBeInTheDocument()
      expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
    }
  )

  test('shows wallet cost and source icon without using subscription metadata', () => {
    renderCost({
      quota: 5000,
      other: { billing_source: 'wallet', subscription_consumed: 12500 },
      showBillingSource: true,
    })

    expect(screen.getByText('$0.01')).toBeVisible()
    expect(screen.getByRole('img', { name: 'Wallet' })).toBeVisible()
    expect(screen.queryByText('Wallet')).not.toBeInTheDocument()
    expect(screen.queryByText('Subscription')).not.toBeInTheDocument()
  })

  test('hides the wallet icon when subscriptions are unavailable', () => {
    renderCost({
      quota: 5000,
      other: { billing_source: 'wallet' },
      showBillingSource: false,
    })

    expect(screen.getByText('$0.01')).toBeVisible()
    expect(screen.queryByText('Wallet')).not.toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  test('keeps the subscription icon on a subscription-billed log when billing sources are hidden', () => {
    renderCost({
      quota: 5000,
      other: { billing_source: 'subscription', subscription_consumed: 12500 },
      showBillingSource: false,
    })

    expect(screen.getByText('$0.025')).toBeVisible()
    expect(screen.getByRole('img', { name: 'Subscription' })).toBeVisible()
    expect(
      screen.queryByRole('img', { name: 'Wallet' })
    ).not.toBeInTheDocument()
  })

  test('keeps legacy cost visible without inventing a funding source', () => {
    renderCost({ quota: 5000, other: null })

    expect(screen.getByText('$0.01')).toBeVisible()
    expect(screen.queryByText('Wallet')).not.toBeInTheDocument()
    expect(screen.queryByText('Subscription')).not.toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  test('keeps a large amount unabridged in a single-line cost bubble', () => {
    renderCost({
      quota: 2147483647,
      other: { billing_source: 'subscription' },
      showBillingSource: true,
    })

    const amount = screen.getByText('$4,294.9673')
    expect(amount).toBeVisible()
    expect(amount).toHaveClass('whitespace-nowrap')
    expect(
      screen.getByRole('button', { name: /Billing Details/ })
    ).toContainElement(amount)
    expect(screen.getByRole('img', { name: 'Subscription' })).toBeVisible()
  })

  test.each([
    { source: 'subscription', label: 'Subscription' },
    { source: 'wallet', label: 'Wallet' },
  ])(
    'vertically centers the $source icon within its container',
    ({ source, label }) => {
      renderCost({
        quota: 5000,
        other: { billing_source: source },
        showBillingSource: true,
      })

      // Mobile cards give inline-flex descendants a minimum height, so the
      // source container must center its smaller icon independently of the badge.
      expect(screen.getByRole('img', { name: label })).toHaveClass(
        'inline-flex',
        'items-center'
      )
    }
  )

  test.each([
    { source: 'subscription', label: 'Subscription' },
    { source: 'wallet', label: 'Wallet' },
  ])(
    'reveals the $source label on hover and keyboard focus',
    async ({ source, label }) => {
      const user = userEvent.setup()
      renderCost({
        quota: 5000,
        other: { billing_source: source },
        showBillingSource: true,
      })

      const marker = screen.getByRole('img', { name: label })
      expect(screen.queryByText(label)).not.toBeInTheDocument()

      await user.hover(marker)
      expect(await screen.findByText(label)).toBeVisible()
      await user.unhover(marker)
      await waitFor(() =>
        expect(screen.queryByText(label)).not.toBeInTheDocument()
      )
      await user.tab()
      expect(marker).toHaveFocus()
      expect(await screen.findByText(label)).toBeVisible()
    }
  )

  test('opens the tool receipt from the stacked icon and reconciles the recorded total', async () => {
    const user = userEvent.setup()
    renderCost({
      quota: 12500,
      other: {
        group_ratio: 2,
        tool_surcharges: [{ name: 'lookup_customer', count: 2, price: 5 }],
      },
    })

    expect(screen.getByText('$0.025')).toBeVisible()
    const marker = screen.getByRole('img', {
      name: 'Tool: lookup_customer',
    })
    await user.click(marker)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('2 × $5 / 1,000 × 2')
    expect(dialog).toHaveTextContent('$0.02')
    expect(dialog).toHaveTextContent('Remaining charge')
    expect(dialog).toHaveTextContent('$0.005')
  })

  test('shows subscription cost and source alongside the legacy surcharge marker', () => {
    renderCost({
      quota: 5000,
      other: {
        billing_source: 'subscription',
        web_search: true,
        web_search_call_count: 1,
        web_search_price: 10,
      },
      showBillingSource: true,
    })

    expect(screen.getByText('$0.01')).toBeVisible()
    expect(screen.getByRole('img', { name: 'Subscription' })).toBeVisible()
    expect(
      screen.getByRole('img', { name: 'Tool: web_search' })
    ).toHaveAttribute('data-tool-surcharge-indicator', 'true')
  })

  test('calculates token categories with the logged tier, token counts and group multiplier', async () => {
    const user = userEvent.setup()
    renderCost({
      quota: 1625,
      other: {
        billing_mode: 'tiered_expr',
        matched_tier: 'standard',
        group_ratio: 0.5,
        expr_b64: btoa('tier("standard", p * 2 + cr * 1 + c * 8)'),
        cache_tokens: 500,
        billing_tokens: { p: 1000, cr: 500, c: 500 },
      },
    })
    await user.click(screen.getByRole('button', { name: /Billing Details/ }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('1,000 × $2 / 1,000,000 × 0.5')
    expect(dialog).toHaveTextContent('500 × $1 / 1,000,000 × 0.5')
    expect(dialog).toHaveTextContent('500 × $8 / 1,000,000 × 0.5')
  })

  const costSnapshot = {
    cost_ratio: 1.3,
    base_quota: 5000,
    cost_quota: 6500,
    revenue_quota: 10000,
    profit_quota: 3500,
    estimated: true as const,
  }

  test('administrator sees the recorded estimated upstream cost and gross profit', () => {
    renderCost({
      quota: 10000,
      other: { admin_info: { cost: costSnapshot } },
      isAdmin: true,
    })
    expect(screen.getByRole('term', { name: 'Upstream cost' })).toBeVisible()
    expect(screen.getByText('$0.013')).toBeVisible()
    expect(screen.getByRole('term', { name: 'Gross profit' })).toBeVisible()
    expect(screen.getByText('$0.007')).toBeVisible()
  })

  test('regular view hides cost estimates even when a response contains admin metadata', async () => {
    renderCost({ quota: 10000, other: { admin_info: { cost: costSnapshot } } })
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: /Billing Details/ }))
    expect(screen.getByRole('dialog')).toHaveTextContent(
      'A reconciled itemized breakdown is unavailable'
    )
    expect(
      screen.queryByRole('term', { name: 'Upstream cost' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText('$0.013')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('term', { name: 'Gross profit' })
    ).not.toBeInTheDocument()
  })

  test.each([true, false])(
    'upstream bill cost and gross profit require administrator view: %s',
    (isAdmin) => {
      renderCost({
        quota: 10000,
        other: {
          admin_info: {
            cost: {
              ...costSnapshot,
              estimated: false,
              source: 'upstream_bill',
            },
          },
        },
        isAdmin,
      })
      if (isAdmin) {
        expect(
          screen.getByRole('term', { name: 'Upstream cost' })
        ).toBeVisible()
        expect(screen.getByRole('term', { name: 'Gross profit' })).toBeVisible()
        expect(screen.getByText('$0.013')).toBeVisible()
        expect(screen.getByText('$0.007')).toBeVisible()
      } else {
        expect(
          screen.queryByRole('term', { name: 'Upstream cost' })
        ).not.toBeInTheDocument()
        expect(
          screen.queryByRole('term', { name: 'Gross profit' })
        ).not.toBeInTheDocument()
        expect(screen.queryByText('$0.013')).not.toBeInTheDocument()
      }
    }
  )

  test.each([
    null,
    { admin_info: { cost: { ...costSnapshot, cost_quota: Number.NaN } } },
    { admin_info: { cost: { ...costSnapshot, estimated: false } } },
  ])(
    'administrator sees missing cost as unrecorded instead of zero',
    (other) => {
      renderCost({ quota: 10000, other, isAdmin: true })
      expect(screen.getAllByText('Not recorded')).toHaveLength(2)
      expect(screen.queryByText('$0')).not.toBeInTheDocument()
    }
  )

  test('administrator sees a negative gross profit and an explicit zero cost correctly', () => {
    const { rerender } = renderCost({
      quota: 5000,
      other: {
        admin_info: {
          cost: { ...costSnapshot, revenue_quota: 5000, profit_quota: -1500 },
        },
      },
      isAdmin: true,
    })
    expect(screen.getByText('-$0.003')).toBeVisible()
    rerender(
      <LogCostDisplay
        quota={10000}
        other={{
          admin_info: {
            cost: {
              ...costSnapshot,
              cost_ratio: 0,
              cost_quota: 0,
              profit_quota: 10000,
            },
          },
        }}
        isAdmin
      />
    )
    expect(screen.getByText('$0')).toBeVisible()
    expect(screen.queryByText('Not recorded')).not.toBeInTheDocument()
  })

  test('subscription cost estimate remains based on the recorded billed usage', () => {
    renderCost({
      quota: 10000,
      other: {
        billing_source: 'subscription',
        subscription_consumed: 10000,
        admin_info: { cost: costSnapshot },
      },
      isAdmin: true,
    })
    expect(screen.getByRole('img', { name: 'Subscription' })).toBeVisible()
    expect(screen.getByText('$0.007')).toBeVisible()
    expect(screen.queryByText('Revenue')).not.toBeInTheDocument()
  })

  test('shows the wallet remainder and free deduction separately for a mixed allowance call', () => {
    renderCost({
      quota: 1500000,
      other: {
        billing_source: 'free_allowance',
        free_quota: 1000000,
        wallet_quota_deducted: 500000,
      },
    })
    expect(screen.getByText('$1')).toBeVisible()
    expect(
      screen.getByRole('term', { name: 'Free allowance deduction' })
    ).toBeVisible()
    expect(screen.getByText('$2')).toBeVisible()
    expect(
      screen.queryByRole('term', { name: 'Gross profit' })
    ).not.toBeInTheDocument()
  })
})
