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
import { cleanup, render, screen, within } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it } from 'vitest'

import {
  getDashboardMetricValue,
  summarizeDashboardFinance,
} from '@/features/dashboard/lib/finance'
import { buildDashboardFlowData } from '@/features/dashboard/lib/flow'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { useAuthStore } from '@/stores/auth-store'

import { FinancialSummary } from '../financial-summary'

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
  expect(screen.getByText('Cost coverage: 50%')).toBeVisible()
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
  expect(screen.queryByText('Cost coverage: 100%')).not.toBeInTheDocument()
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
