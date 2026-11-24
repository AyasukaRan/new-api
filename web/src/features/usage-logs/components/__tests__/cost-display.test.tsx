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
import { render, screen, waitFor, within } from '@testing-library/react'
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

import { usageLogSchema } from '../../data/schema'
import { getChargeBreakdown } from '../../lib/charge-breakdown'
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

  test.each(['amount', 'tool'])(
    'hovering the %s shows every billed tool and its recorded count without clicking',
    async (target) => {
      const user = userEvent.setup()
      renderCost({
        quota: 12500,
        other: {
          group_ratio: 2,
          free_quota: 2500,
          wallet_quota_deducted: 10000,
          tool_surcharges: [
            { name: 'mcp__files__find_in_workspace', count: 2, price: 5 },
            { name: 'web_search', count: 1, price: 2 },
          ],
        },
      })
      const amount = screen.getByRole('button', { name: /Billing Details/ })
      const tool = screen.getByRole('img', { name: 'Tool: web_search' })
      await user.hover(target === 'amount' ? amount : tool)
      const panel = await screen.findByRole('dialog', {
        name: 'Billing Details',
      })
      const chart = within(panel).getByRole('img', { name: 'Cost breakdown' })
      expect(chart).toBeVisible()
      expect(
        within(chart).getByTitle('mcp__files__find_in_workspace')
      ).toHaveStyle({ width: '80%' })
      expect(panel).toHaveTextContent('Total Cost$0.025')
      expect(panel).toHaveTextContent('Amount charged$0.02')
      const rows = within(panel).getByRole('list', { name: 'Cost breakdown' })
      expect(rows).toHaveTextContent(/mcp__files__find_in_workspace\s*×2/)
      expect(rows).toHaveTextContent(/web_search\s*×1/)
      expect(rows).toHaveTextContent('$0.02')
      expect(rows).not.toHaveTextContent('/ 1,000 times')
      await user.keyboard('{Escape}')
      await waitFor(() => expect(panel).not.toBeInTheDocument())
    }
  )

  test('opens the linked request from the amount panel on the same log', async () => {
    const user = userEvent.setup()
    renderCost({
      quota: 100,
      log: usageLogSchema.parse({
        id: 48,
        user_id: 3,
        created_at: 1795400000,
        type: 2,
        content: '',
        model_name: 'deepseek-flash',
        quota: 100,
      }),
      other: null,
    })
    await user.click(screen.getByRole('button', { name: /Billing Details/ }))
    const panel = await screen.findByRole('dialog', { name: 'Billing Details' })
    await user.click(
      within(panel).getByRole('button', { name: 'Request details' })
    )
    const dialog = await screen.findByRole('dialog', {
      name: 'Request details',
    })
    expect(within(dialog).getByText(/deepseek-flash/)).toBeVisible()
    expect(
      within(dialog).getByRole('tab', { name: 'Cost breakdown' })
    ).toHaveAttribute('aria-selected', 'true')
    await user.click(
      within(dialog).getByRole('tab', { name: 'Timing breakdown' })
    )
    expect(
      within(dialog).getByRole('tabpanel', { name: 'Timing breakdown' })
    ).toBeVisible()
  })

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
    const preview = screen.getByRole('dialog', { name: 'Billing Details' })
    await user.click(
      within(preview).getByRole('button', { name: 'Request details' })
    )
    const dialog = screen.getByRole('dialog', { name: 'Request details' })
    expect(dialog).toHaveTextContent('2 times × $5 / 1,000 times × 2')
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
    const preview = screen.getByRole('dialog', { name: 'Billing Details' })
    await user.click(
      within(preview).getByRole('button', { name: 'Request details' })
    )
    const dialog = screen.getByRole('dialog', { name: 'Request details' })
    expect(dialog).toHaveTextContent(
      '1,000 tokens × $2 / 1,000,000 tokens × 0.5'
    )
    expect(dialog).toHaveTextContent('500 tokens × $1 / 1,000,000 tokens × 0.5')
    expect(dialog).toHaveTextContent('500 tokens × $8 / 1,000,000 tokens × 0.5')
  })

  test('reconciles a recorded DeepSeek off-peak receipt without a normalized token snapshot', () => {
    const other = {
      billing_mode: 'tiered_expr',
      matched_tier: 'off_peak',
      group_ratio: 0.2,
      cache_tokens: 9856,
      expr_b64: btoa(
        'hour("Asia/Shanghai") < 12 ? tier("peak", p * 2 + cr * 0.04 + c * 8) : tier("off_peak", p * 1 + cr * 0.02 + c * 4)'
      ),
    }
    const log = { prompt_tokens: 34666, completion_tokens: 73 }
    const lines = getChargeBreakdown(other, 2530, 500_000, [], log)
    expect(lines.map((l) => [l.label, l.basis?.count])).toEqual([
      ['Uncached input', 24810],
      ['Cache Read', 9856],
      ['Output', 73],
    ])
    expect(lines[0].quota).toBeCloseTo(2481)
    expect(lines[1].quota).toBeCloseTo(19.712)
    expect(lines[2].quota).toBeCloseTo(29.2)
    expect(getChargeBreakdown(other, 2529, 500_000, [], log)).toEqual([])
  })

  test('keeps historical Claude cache writes and reads separate from its uncached input', () => {
    const other = {
      billing_mode: 'tiered_expr',
      matched_tier: 'standard',
      group_ratio: 1.5,
      usage_semantic: 'anthropic',
      cache_tokens: 236537,
      cache_creation_tokens: 1141,
      cache_creation_tokens_5m: 1141,
      expr_b64: btoa(
        'tier("standard", p * 4 + c * 20 + cr * 0.2 + cc * 5 + cc1h * 8)'
      ),
    }
    const lines = getChargeBreakdown(other, 41202, 500_000, [], {
      prompt_tokens: 96,
      completion_tokens: 77,
    })
    expect(lines.map((l) => [l.label, l.basis?.count])).toEqual([
      ['Uncached input', 96],
      ['Cache Read', 236537],
      ['Cache Write (5m)', 1141],
      ['Output', 77],
    ])
    expect(Math.round(lines.reduce((n, l) => n + l.quota, 0))).toBe(41202)
  })

  test('does not infer missing image counts or itemize a settlement that does not reconcile', () => {
    const other = {
      billing_mode: 'tiered_expr',
      matched_tier: 'base',
      group_ratio: 1,
      expr_b64: btoa('tier("base", p * 2 + img * 2)'),
    }
    expect(
      getChargeBreakdown(other, 100, 500_000, [], {
        prompt_tokens: 100,
        completion_tokens: 0,
      })
    ).toEqual([])
    expect(
      getChargeBreakdown(
        { ...other, billing_tokens: { p: 100 } },
        100,
        500_000,
        []
      )
    ).toEqual([])
    expect(
      getChargeBreakdown(
        {
          ...other,
          billing_quota_per_unit: 2_000_000,
          billing_tokens: { p: 99, img: 1 },
        },
        400,
        500_000,
        []
      )
    ).toHaveLength(2)
  })

  const costSnapshot = {
    cost_ratio: 1.3,
    base_quota: 5000,
    cost_quota: 6500,
    revenue_quota: 10000,
    profit_quota: 3500,
    estimated: true as const,
  }

  test('wallet charges and their accounting lines share icon and right-aligned amount tracks', () => {
    renderCost({
      quota: 10000,
      other: {
        billing_source: 'wallet',
        tool_surcharges: [{ name: 'web_search', count: 2, price: 1 }],
        admin_info: { cost: costSnapshot },
      },
      showBillingSource: true,
      isAdmin: true,
    })

    const wallet = screen.getByRole('img', { name: 'Wallet' })
    const charge = screen.getByRole('button', { name: /Billing Details/ })
    const details = screen
      .getByRole('term', { name: 'Upstream cost' })
      .closest('dl')
    const marker = screen.getByRole('img', { name: 'Tool: web_search' })
    const amount = screen.getByText('$0.02')
    expect(charge).toContainElement(marker)
    expect(
      amount.compareDocumentPosition(marker) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
    expect(marker.closest('[data-fee-tool-lanes]')).toHaveAttribute(
      'data-fee-tool-lanes',
      '1'
    )
    expect(wallet.parentElement).toBe(details?.parentElement)
    expect(charge).toHaveClass('col-start-2', 'grid-cols-subgrid')
    expect(details).toHaveClass('grid-cols-subgrid', 'text-end')
  })

  test('an observed tool with no positive surcharge leaves no trailing tool slot', () => {
    renderCost({
      quota: 10000,
      other: {
        invoked_tools: ['web_search'],
        tool_surcharges: [{ name: 'web_search', count: 1, price: 0 }],
      },
    })

    const charge = screen.getByRole('button', { name: /Billing Details/ })
    expect(charge).toHaveClass('col-start-2', 'justify-self-end')
    expect(charge.querySelector('[data-fee-tool-lanes]')).toBeNull()
    expect(
      screen.queryByRole('img', { name: 'Tool: web_search' })
    ).not.toBeInTheDocument()
  })

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
    await userEvent.setup().tab()
    expect(
      screen.getByRole('button', { name: /Billing Details/ })
    ).toHaveFocus()
    expect(
      within(screen.getByRole('dialog')).queryByRole('img', {
        name: 'Cost breakdown',
      })
    ).not.toBeInTheDocument()
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
