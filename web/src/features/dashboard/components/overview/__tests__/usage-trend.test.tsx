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
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import dayjs from '@/lib/dayjs'
import { formatLogQuota } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import {
  summarizePersonalUsage,
  summarizeUsageRows,
} from '../../../hooks/use-personal-usage'
import { UsageEfficiency } from '../usage-efficiency'
import { UsageTrend } from '../usage-trend'

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

it('preserves empty time buckets and sums hourly model rows for both views', () => {
  const day = dayjs('2026-11-18').startOf('day')
  const hour = Math.ceil(day.unix() / 3600) * 3600
  const rows = [
    {
      created_at: hour,
      count: 2,
      quota: 3,
      token_used: 5,
      cache_read_tokens: 17,
      cache_creation_tokens: 4,
    },
    {
      created_at: hour,
      count: 7,
      quota: 1,
      token_used: 1,
      cache_read_tokens: 8,
      cache_creation_tokens: 0,
    },
    {
      created_at: hour + 7200,
      count: 1,
      token_used: 2,
      cache_creation_tokens: 3,
    },
    { created_at: hour - 3600, count: 100 },
  ]
  const hourly = summarizePersonalUsage('24h', hour, hour + 23 * 3600, rows)
  expect(hourly.points).toHaveLength(24)
  expect(hourly.points.slice(0, 3)).toEqual([
    {
      time: hour,
      requests: 9,
      usage: 4,
      tokens: 6,
      cacheRead: 25,
      cacheWrite: 4,
    },
    {
      time: hour + 3600,
      requests: 0,
      usage: 0,
      tokens: 0,
      cacheRead: 0,
      cacheWrite: 0,
    },
    {
      time: hour + 7200,
      requests: 1,
      usage: 0,
      tokens: 2,
      cacheRead: null,
      cacheWrite: 3,
    },
  ])
  expect(hourly.totals).toEqual({
    requests: 10,
    usage: 4,
    tokens: 8,
    cacheRead: null,
    cacheWrite: 7,
  })
  const weekly = summarizePersonalUsage(
    '7d',
    day.unix(),
    day.add(6, 'day').endOf('day').unix(),
    [...rows, { created_at: day.add(6, 'day').add(2, 'hour').unix(), count: 4 }]
  )
  expect(weekly.points.map((point) => point.requests)).toEqual([
    10, 0, 0, 0, 0, 0, 4,
  ])
  expect(weekly.totals).toEqual({
    requests: 14,
    usage: 4,
    tokens: 8,
    cacheRead: null,
    cacheWrite: null,
  })
})

it('shares the active personal time window with the model ranking, including for admins', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 8, username: 'owner', role: 100, quota: 0 })
  const get = vi.spyOn(api, 'get').mockImplementation(async (_url, config) => {
    const params = config?.params as {
      end_timestamp: number
      default_time: string
    }
    const time = params.end_timestamp - (params.end_timestamp % 3600)
    const rows =
      params.default_time === 'hour'
        ? [
            {
              created_at: time,
              count: 5,
              model_name: 'A',
              token_used: 8,
              quota: 100000,
            },
            { created_at: time - 3600, count: 5, model_name: 'A' },
            ...['B', 'C', 'D', 'E', 'F', 'G'].map((model_name) => ({
              created_at: time,
              count: 1,
              token_used: model_name === 'G' ? 75 : 1,
              quota: model_name === 'B' ? 1000000 : 0,
              model_name,
            })),
          ]
        : [
            {
              created_at: time,
              count: 4,
              token_used: 400,
              model_name: 'week-only-model',
            },
          ]
    return { data: { success: true, data: rows } }
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <UsageTrend />
    </QueryClientProvider>
  )

  const requestTotal = await screen.findByRole('button', {
    name: /Requests 16/,
  })
  const tokens = screen.getByRole('button', { name: /Tokens 88/ })
  expect(tokens).toHaveAttribute('aria-pressed', 'true')
  const models = screen.getByRole('table', { name: 'Model hotspots' })
  const tokenLeader = within(models).getAllByRole('row')[1]
  expect(
    within(tokenLeader).getByRole('progressbar', { name: 'G' })
  ).toHaveAttribute('aria-valuetext', '85.2%')
  expect(within(tokenLeader).getByRole('cell', { name: '75' })).toBeVisible()
  await user.click(requestTotal)
  expect(requestTotal).toHaveAttribute('aria-pressed', 'true')
  expect(
    within(models).getByRole('progressbar', { name: 'A' })
  ).toHaveAttribute('aria-valuetext', '62.5%')
  expect(
    within(models).getByRole('progressbar', { name: 'Other models' })
  ).toHaveAttribute('aria-valuetext', '12.5%')
  await user.click(within(models).getByRole('button', { name: 'Billed usage' }))
  expect(requestTotal).toHaveAttribute('aria-pressed', 'true')
  expect(
    within(models).getByRole('columnheader', { name: 'Billed usage' })
  ).toHaveAttribute('aria-sort', 'descending')
  expect(
    within(within(models).getAllByRole('row')[1]).getByRole('rowheader')
  ).toHaveTextContent('B')
  await user.click(tokens)
  expect(tokens).toHaveAttribute('aria-pressed', 'true')
  expect(get).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: 'Last 7 days' }))
  expect(
    await screen.findByRole('button', { name: /Requests 4/ })
  ).toBeVisible()
  expect(
    screen.getByRole('progressbar', { name: 'week-only-model' })
  ).toBeVisible()
  expect(get.mock.calls.map(([url]) => url)).toEqual([
    '/api/data/self',
    '/api/data/self',
  ])
  act(() => client.clear())
})

it('computes per-request efficiency from summed usage, preserves missing cache values and omits unavailable ratios', async () => {
  const summary = summarizeUsageRows(
    [
      {
        created_at: 1,
        count: 9,
        token_used: 700,
        quota: 1000000,
        cache_read_tokens: 0,
        cache_creation_tokens: 4,
      },
      {
        created_at: 3,
        count: 1,
        token_used: 300,
        quota: 4000000,
        cache_creation_tokens: 6,
      },
      { created_at: 6, count: 1, token_used: 999, quota: 99000000 },
    ],
    {
      start_timestamp: 0,
      end_timestamp: 4,
      buckets: [
        { start: 0, end: 2 },
        { start: 2, end: 5 },
      ],
    }
  )
  const { rerender } = render(<UsageEfficiency data={summary} />)
  const billed = formatLogQuota(500000)
  expect(screen.getByText(billed)).toBeVisible()
  expect(screen.getByText('100')).toBeVisible()
  expect(screen.getByText('10')).toBeVisible()
  expect(screen.getByText('Unavailable')).toBeVisible()
  const toggle = screen.getByRole('button', { name: 'Usage per request' })
  await userEvent.click(toggle)
  expect(toggle).toHaveAttribute('aria-pressed', 'true')
  rerender(
    <UsageEfficiency
      data={{
        ...summary,
        totals: {
          tokens: 0,
          requests: 0,
          usage: 0,
          cacheRead: 0,
          cacheWrite: 0,
        },
      }}
    />
  )
  expect(screen.getAllByText('Unavailable')).toHaveLength(2)
})

it('offers a retry on a failed time range after an empty personal result', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 8, username: 'owner', role: 100, quota: 0 })
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: [] } })
  render(
    <QueryClientProvider client={client}>
      <UsageTrend />
    </QueryClientProvider>
  )
  await screen.findByText('No Data')
  get.mockRejectedValue(new Error('Service unavailable'))
  await userEvent.click(screen.getByRole('button', { name: 'Last 7 days' }))
  await screen.findByRole('button', { name: /retry/i })
  act(() => client.clear())
})
