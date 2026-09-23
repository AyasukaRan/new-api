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
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { useState } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { buildChartTimeDomain } from '@/features/dashboard/lib/charts'
import type {
  DashboardFilters,
  ModelAnalyticsChartTab,
} from '@/features/dashboard/types'
import { api } from '@/lib/api'
import { computeTimeRange } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { CallSources } from '../call-sources'
import { ModelCharts } from '../model-charts'

// VChart needs a canvas; retain the real chart processing and inspect its output.
vi.mock('@visactor/react-vchart', () => ({
  VChart: (props: { spec: { type: string; data: unknown } }) => (
    <output aria-label={`Rendered ${props.spec.type} chart`}>
      {JSON.stringify(props.spec.data)}
    </output>
  ),
}))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: vi.fn() },
}))

let client: QueryClient
const filters: DashboardFilters = {
  start_timestamp: new Date('2026-09-22T00:00:00Z'),
  end_timestamp: new Date('2026-09-23T00:00:00Z'),
  time_granularity: 'hour',
  username: 'other-user',
}
const rows = [
  {
    client_tool: 'DeepSeek Harness',
    created_at: 1790035200,
    count: 3,
    token_used: 1200,
    quota: 500000,
  },
  {
    client_tool: '',
    created_at: 1790038800,
    count: 1,
    token_used: 100,
    quota: 0,
  },
]

beforeEach(async () => {
  localStorage.clear()
  await i18next.changeLanguage('en')
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useAuthStore
    .getState()
    .auth.setUser({ id: 7, username: 'member', role: 1, quota: 0 })
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
})

afterEach(async () => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  await i18next.changeLanguage('en')
  localStorage.clear()
})

function renderSources(currentFilters = filters) {
  return render(
    <QueryClientProvider client={client}>
      <CallSources filters={currentFilters} />
    </QueryClientProvider>
  )
}

it('groups hourly source rows once for the table while retaining each hour in the chart', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        ...rows,
        {
          client_tool: 'DeepSeek Harness',
          created_at: 1790038800,
          count: 2,
          token_used: 800,
          quota: 500000,
        },
      ],
    },
  })
  renderSources()

  const table = await screen.findByRole('table', { name: 'Call Sources' })
  const identified = within(table).getByRole('row', {
    name: /DeepSeek Harness/,
  })
  expect(within(identified).getByText('5')).toBeVisible()
  expect(within(identified).getByText('2,000')).toBeVisible()
  expect(within(identified).getByText('$2')).toBeVisible()
  expect(within(table).getAllByRole('row')).toHaveLength(3)
  const chart = await screen.findByLabelText('Rendered area chart')
  const series = JSON.parse(chart.textContent ?? '[]')[0].values as Array<{
    Model: string
    Count: number
  }>
  expect(
    series
      .filter((row) => row.Model === 'DeepSeek Harness')
      .map((row) => row.Count)
      .filter(Boolean)
  ).toEqual([3, 2])
  expect(
    series
      .filter((row) => row.Model === 'Unidentified source')
      .reduce((sum, row) => sum + row.Count, 0)
  ).toBe(1)
  expect(get).toHaveBeenCalledTimes(1)
})

function SynchronizedCharts(props: { filters: DashboardFilters }) {
  const [activeTab, setActiveTab] = useState<ModelAnalyticsChartTab>('top')
  const timeDomain = buildChartTimeDomain(
    computeTimeRange(
      1,
      props.filters.start_timestamp,
      props.filters.end_timestamp
    ),
    props.filters.time_granularity
  )
  return (
    <QueryClientProvider client={client}>
      <CallSources
        filters={props.filters}
        timeDomain={timeDomain}
        activeTab={activeTab}
        onActiveTabChange={setActiveTab}
      />
      <ModelCharts
        data={[
          { model_name: 'deepseek-flash', created_at: 1790035200, count: 4 },
        ]}
        timeDomain={timeDomain}
        activeTab={activeTab}
        onActiveTabChange={setActiveTab}
      />
    </QueryClientProvider>
  )
}

it('keeps source and model chart types synchronized in both directions without refetching', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: rows } })
  const user = userEvent.setup()
  render(<SynchronizedCharts filters={filters} />)
  const sources = await screen.findByRole('group', { name: 'Call Sources' })
  const models = screen.getByRole('group', { name: 'Model Call Analytics' })
  expect(
    within(sources).getByRole('button', { name: 'Call Count Ranking' })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(
    within(models).getByRole('button', { name: 'Call Count Ranking' })
  ).toHaveAttribute('aria-pressed', 'true')

  await user.click(
    within(sources).getByRole('button', { name: 'Call Count Distribution' })
  )
  expect(
    within(models).getByRole('button', { name: 'Call Count Distribution' })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(
    await within(models).findByLabelText('Rendered pie chart')
  ).toBeVisible()
  expect(
    await within(sources).findByLabelText('Rendered pie chart')
  ).toBeVisible()

  await user.click(within(models).getByRole('button', { name: 'Call Trend' }))
  expect(
    within(sources).getByRole('button', { name: 'Call Trend' })
  ).toHaveAttribute('aria-pressed', 'true')
  const sourceChart = await within(sources).findByLabelText(
    'Rendered area chart'
  )
  const modelChart = await within(models).findByLabelText('Rendered area chart')
  const sourceTimes = (
    JSON.parse(sourceChart.textContent ?? '[]')[0].values as Array<{
      Time: string
    }>
  ).map((row) => row.Time)
  const modelTimes = (
    JSON.parse(modelChart.textContent ?? '[]')[0].values as Array<{
      Time: string
    }>
  ).map((row) => row.Time)
  expect([...new Set(sourceTimes)]).toEqual([...new Set(modelTimes)])
  expect(get).toHaveBeenCalledTimes(1)
})

it('includes unidentified historical requests in the shares and preserves free usage', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: rows },
  })
  renderSources()
  const table = await screen.findByRole('table', { name: 'Call Sources' })
  const identified = within(table).getByRole('row', {
    name: /DeepSeek Harness/,
  })
  expect(within(identified).getByText('3')).toBeVisible()
  expect(within(identified).getByText('1,200')).toBeVisible()
  expect(within(identified).getByText('$1')).toBeVisible()
  expect(within(identified).getByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '75'
  )
  const unknown = within(table).getByRole('row', {
    name: /Unidentified source/,
  })
  expect(within(unknown).getByText('25%')).toBeVisible()
  expect(within(unknown).getByText('$0')).toBeVisible()
})

it('updates displayed costs when the configured currency changes without reloading source data', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: rows } })
  renderSources()
  const table = await screen.findByRole('table', { name: 'Call Sources' })
  expect(within(table).getByText('$1')).toBeVisible()

  await act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...useSystemConfigStore.getState().config.currency,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
      },
    })
  )

  expect(await within(table).findByText('¥7')).toBeVisible()
  expect(within(table).queryByText('$1')).not.toBeInTheDocument()
  expect(get).toHaveBeenCalledTimes(1)
})

it('uses the self endpoint without username and sends the selected exact time range', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: rows } })
  renderSources()
  await screen.findByRole('table')
  expect(get).toHaveBeenCalledWith('/api/data/sources/self', {
    params: {
      start_timestamp: 1790035200,
      end_timestamp: 1790121600,
      time_series: true,
    },
  })
})

it('follows the administrator username filter and refreshes the displayed sources', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: 10, quota: 0 })
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: rows } })
  const view = renderSources()
  await screen.findByRole('table')
  expect(get).toHaveBeenCalledWith('/api/data/sources', {
    params: {
      start_timestamp: 1790035200,
      end_timestamp: 1790121600,
      time_series: true,
      username: 'other-user',
    },
  })
  get.mockResolvedValue({
    data: {
      success: true,
      data: [
        { client_tool: 'Python Requests', count: 1, token_used: 2, quota: 0 },
      ],
    },
  })
  view.rerender(
    <QueryClientProvider client={client}>
      <CallSources filters={{ ...filters, username: 'second-user' }} />
    </QueryClientProvider>
  )
  expect(await screen.findByText('Python Requests')).toBeVisible()
  expect(screen.queryByText('DeepSeek Harness')).not.toBeInTheDocument()
  expect(get).toHaveBeenLastCalledWith('/api/data/sources', {
    params: {
      start_timestamp: 1790035200,
      end_timestamp: 1790121600,
      time_series: true,
      username: 'second-user',
    },
  })
  get.mockResolvedValue({ data: { success: true, data: rows } })
  await userEvent.setup().click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByText('DeepSeek Harness')).toBeVisible()
})

it('clears the previous account sources while the next account is loading', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true, data: rows } })
  renderSources()
  await screen.findByRole('table')
  let complete: (value: unknown) => void = () => undefined
  const pending = new Promise((resolve) => {
    complete = resolve
  })
  get.mockImplementation(() => pending as never)
  await act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 8, username: 'next-member', role: 1, quota: 0 })
  )
  expect(await screen.findByRole('status')).toHaveTextContent('Loading...')
  expect(screen.queryByText('DeepSeek Harness')).not.toBeInTheDocument()
  await act(() => complete({ data: { success: true, data: [] } }))
  expect(await screen.findByText('No data available')).toBeVisible()
})

it.each([
  { success: false, message: 'Service unavailable' },
  { success: true, data: null },
])(
  'shows a retryable error rather than zero usage for an unsuccessful or incomplete response: %j',
  async (response) => {
    const get = vi.spyOn(api, 'get').mockResolvedValue({ data: response })
    renderSources()
    const retry = await screen.findByRole('button', { name: 'Retry' })
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.queryByText('No data available')).not.toBeInTheDocument()
    get.mockResolvedValue({ data: { success: true, data: rows } })
    await userEvent.setup().click(retry)
    expect(await screen.findByRole('table')).toBeVisible()
  }
)

it('keeps the panel empty when the selected range has no usage', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: [] } })
  renderSources()
  expect(await screen.findByText('No data available')).toBeVisible()
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
})

it('uses a bounded scrolling table for long source names and displays zero shares safely', async () => {
  const source =
    'Custom automation source with a very long descriptive client name'
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [{ client_tool: source, count: 0, token_used: 0, quota: 0 }],
    },
  })
  renderSources()
  const table = await screen.findByRole('table')
  expect(within(table).getByText(source)).toBeInTheDocument()
  expect(table.parentElement).toHaveClass('overflow-auto', 'max-h-[420px]')
  expect(within(table).getByRole('progressbar')).toHaveAttribute(
    'aria-valuenow',
    '0'
  )
  expect(within(table).getByText('0%')).toBeVisible()
})

it.each([
  ['zhCN', 'zh-CN'],
  ['zhTW', 'zh-TW'],
  ['en', 'en'],
  ['fr', 'fr'],
  ['ja', 'ja'],
  ['ru', 'ru'],
  ['vi', 'vi'],
  ['bad_locale', undefined],
] as const)(
  'updates source numbers after switching to %s without invalid Intl locale errors',
  async (language, locale) => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: rows },
    })
    renderSources()
    await screen.findByRole('table')
    i18next.addResourceBundle(language, 'translation', {
      'Call Sources': 'Call Sources',
    })
    await act(() => i18next.changeLanguage(language))
    const row = within(screen.getByRole('table')).getByRole('row', {
      name: /DeepSeek Harness/,
    })
    await waitFor(() =>
      expect(
        within(row).getByText(new Intl.NumberFormat(locale).format(1200), {
          normalizer: (value) => value,
        })
      ).toBeVisible()
    )
  }
)
