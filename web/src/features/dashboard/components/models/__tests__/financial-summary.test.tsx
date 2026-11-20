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
import { act, cleanup, render, screen, within } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import {
  getDashboardMetricValue,
  hourlyDashboardFinance,
  summarizeDashboardFinance,
} from '@/features/dashboard/lib/finance'
import { buildDashboardFlowData } from '@/features/dashboard/lib/flow'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import {
  FinancialSummary,
  OverviewFinancialSummary,
} from '../financial-summary'

const data: QuotaDataItem[] = [
  {
    created_at: 0,
    count: 1,
    quota: 500000,
    finance: {
      cost_quota: 650000,
      profit_quota: -150000,
      covered_revenue_quota: 500000,
      recorded_count: 1,
      actual_count: 1,
      estimated_count: 0,
      unrecorded_count: 0,
    },
  },
  { created_at: 3600, count: 1, quota: 900000 },
]

beforeEach(async () => {
  localStorage.clear()
  await i18next.changeLanguage('en')
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: 10, quota: 0 })
})

afterEach(() => {
  vi.restoreAllMocks()
  cleanup()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
})

it('summarizes only recorded costs and keeps unrecorded revenue outside gross profit', () => {
  expect(summarizeDashboardFinance(data)).toEqual({
    revenue_quota: 1400000,
    count: 2,
    cost_quota: 650000,
    profit_quota: -150000,
    covered_revenue_quota: 500000,
    recorded_count: 1,
    actual_count: 1,
    estimated_count: 0,
    unrecorded_count: 1,
  })
  render(<FinancialSummary data={data} />)
  expect(
    within(screen.getByRole('group', { name: 'Gross profit' })).getByText(
      '-$0.3'
    )
  ).toBeVisible()
  expect(
    screen.getByRole('progressbar', { name: 'Cost coverage' })
  ).toHaveAttribute('aria-valuenow', '50')
  expect(screen.getByText('-30%')).toBeVisible()
  expect(
    screen.getByText(
      'Cost and profit include only requests with recorded costs.'
    )
  ).toBeVisible()
})

it('shows no cost data for normal users even if a caller supplies finance fields', () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'fp', role: 1, quota: 0 })
  render(<FinancialSummary data={data} />)
  expect(
    screen.queryByRole('region', { name: 'Financial summary' })
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Gross profit')).not.toBeInTheDocument()
})

it('does not warn about missing history when all costs are recorded', () => {
  render(<FinancialSummary data={[data[0]]} />)
  expect(
    screen.getByRole('progressbar', { name: 'Cost coverage' })
  ).toHaveAttribute('aria-valuenow', '100')
  expect(
    screen.queryByText(
      'Cost and profit include only requests with recorded costs.'
    )
  ).not.toBeInTheDocument()
})

it('marks wholly missing cost as unrecorded rather than zero', () => {
  render(<FinancialSummary data={[data[1]]} />)
  expect(
    within(screen.getByRole('group', { name: 'Upstream cost' })).getByText(
      'Not recorded'
    )
  ).toBeVisible()
  expect(
    within(screen.getByRole('group', { name: 'Gross profit' })).getByText(
      'Not recorded'
    )
  ).toBeVisible()
})

it('excludes free allowances from revenue even when cost is not recorded', () => {
  const rows: QuotaDataItem[] = [
    {
      created_at: 0,
      count: 2,
      quota: 1500000,
      finance: {
        cost_quota: 0,
        profit_quota: 0,
        covered_revenue_quota: 0,
        recorded_count: 0,
        actual_count: 0,
        estimated_count: 0,
        unrecorded_count: 2,
        free_quota: 1000000,
      },
    },
  ]
  expect(summarizeDashboardFinance(rows).revenue_quota).toBe(500000)
  expect(getDashboardMetricValue(rows[0], 'quota')).toBe(500000)
  expect(getDashboardMetricValue(rows[0], 'requests')).toBe(2)
  expect(getDashboardMetricValue(rows[0], 'profit')).toBeNull()
  render(<FinancialSummary data={rows} />)
  expect(
    within(screen.getByRole('group', { name: 'Revenue' })).getByText('$1')
  ).toBeVisible()
})

it('preserves free deductions when aggregating flow rows into the financial summary', () => {
  if (!data[0].finance) throw new Error('Missing finance fixture')
  const finance = {
    ...data[0].finance,
    free_quota: 250000,
    profit_quota: -400000,
    covered_revenue_quota: 250000,
  }
  const flow = buildDashboardFlowData(
    [
      {
        user_id: 1,
        username: 'member',
        model_name: 'deepseek-flash',
        channel_id: 2,
        use_group: 'DeepSeek',
        quota: 500000,
        count: 1,
        token_used: 100,
        finance,
      },
    ],
    'quota',
    { role: 'admin' }
  )
  expect(flow.summary.quota).toBe(500000)
  expect(flow.summary.finance?.free_quota).toBe(250000)
  expect(
    summarizeDashboardFinance([
      {
        quota: flow.summary.quota,
        count: flow.summary.requests,
        finance: flow.summary.finance,
      },
    ]).revenue_quota
  ).toBe(250000)
})

it('keeps free usage out of hourly revenue, negative profit visible and partial or absent costs as gaps', () => {
  const snapshot = data[0].finance
  if (!snapshot) throw new Error('Missing finance fixture')
  const points = hourlyDashboardFinance(
    [
      {
        ...data[0],
        created_at: 3700,
        finance: {
          ...snapshot,
          free_quota: 250000,
          profit_quota: -400000,
          covered_revenue_quota: 250000,
        },
      },
      { ...data[1], created_at: 7200 },
      { ...data[0], created_at: 10900 },
      { ...data[1], created_at: 10910 },
      { ...data[0], created_at: 11001 },
    ],
    0,
    11000
  )
  expect(points).toEqual([
    { time: 0, revenue: 0, cost: 0, profit: 0 },
    { time: 3600, revenue: 250000, cost: 650000, profit: -400000 },
    { time: 7200, revenue: 900000, cost: null, profit: null },
    { time: 10800, revenue: 1400000, cost: null, profit: null },
  ])
})

it('reads sitewide finance in aligned 24 or 168 hourly buckets and only for administrators', async () => {
  const now = new Date('2026-11-20T11:27:03Z')
  vi.spyOn(Date, 'now').mockReturnValue(now.getTime())
  const end = now.getTime() / 1000
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({
      data: {
        success: true,
        data: [{ ...data[0], created_at: end - (end % 3600) }],
      },
    })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const mount = (hours: 24 | 168) => (
    <QueryClientProvider client={client}>
      <OverviewFinancialSummary hours={hours} />
    </QueryClientProvider>
  )
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'member', role: 1, quota: 0 })
  const { rerender } = render(mount(24))
  expect(get).not.toHaveBeenCalled()
  expect(
    screen.queryByRole('region', { name: 'Financial summary' })
  ).not.toBeInTheDocument()
  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'owner', role: 10, quota: 0 })
  )
  expect(
    await screen.findByRole('figure', { name: 'Financial summary · Trend' })
  ).toBeVisible()
  rerender(mount(168))
  expect(
    await screen.findByRole('figure', { name: 'Financial summary · Trend' })
  ).toBeVisible()
  expect(get.mock.calls).toEqual(
    [24, 168].map((hours) => [
      '/api/data/',
      {
        params: {
          start_timestamp: end - (end % 3600) - (hours - 1) * 3600,
          end_timestamp: end,
          default_time: 'hour',
        },
      },
    ])
  )
  client.clear()
})
