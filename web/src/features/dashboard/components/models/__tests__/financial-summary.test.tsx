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

import { summarizeDashboardFinance } from '@/features/dashboard/lib/finance'
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
