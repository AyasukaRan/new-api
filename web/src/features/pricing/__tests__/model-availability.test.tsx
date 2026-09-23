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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ThemeProvider } from '@/context/theme-provider'
import type { PerformanceMetricsData } from '@/features/performance-metrics/types'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelDetailsContent } from '../components/model-details'
import {
  ModelChannelAvailability,
  ModelDetailsPerformance,
} from '../components/model-details-performance'
import type { PricingModel } from '../types'

// Canvas rendering is exercised by the browser preview; these tests cover the
// API-to-UI availability contract and interactions around that renderer.
const renderChart = vi.hoisted(() =>
  vi.fn<(props: { spec: unknown }) => null>(() => null)
)
vi.mock('@visactor/react-vchart', () => ({ VChart: renderChart }))

const model: PricingModel = {
  id: 1,
  model_name: 'example-model',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
}
let queryClient: QueryClient
beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
})
afterEach(() => {
  queryClient.clear()
  useAuthStore.getState().auth.reset()
})

function renderPerformance(data?: PerformanceMetricsData['data']) {
  if (data) {
    queryClient.setQueryData(['perf-metrics', model.model_name], {
      success: true,
      data,
    })
  }
  return render(
    <ThemeProvider defaultTheme='light'>
      <QueryClientProvider client={queryClient}>
        <ModelDetailsPerformance model={model} />
      </QueryClientProvider>
    </ThemeProvider>
  )
}

function group(successRate = 50) {
  return {
    group: 'default',
    success_rate: successRate,
    avg_latency_ms: 1500,
    avg_ttft_ms: 500,
    avg_tps: 12,
    series: [],
  }
}

describe('model availability details', () => {
  it.each([
    {
      current_available: true,
      status: 'Currently available',
      color: 'text-success',
    },
    {
      current_available: false,
      status: 'Currently unavailable',
      color: 'text-destructive',
    },
    {
      current_available: undefined,
      status: 'Waiting for update',
      color: 'text-muted-foreground',
    },
  ])(
    'keeps $status independent of history in overview and performance',
    async ({ current_available, status, color }) => {
      queryClient.setQueryData(['perf-metrics', model.model_name], {
        success: true,
        data: {
          model_name: model.model_name,
          current_available,
          current_observed_at: 1788782400,
          avg_latency_ms: 2100,
          avg_tps: 23,
          availability_rate: 82,
          availability_series: [{ ts: 1788782400, success_rate: 82 }],
          groups: [
            {
              ...group(82),
              series: [
                {
                  ts: 1788782400,
                  success_rate: 82,
                  avg_ttft_ms: 500,
                  avg_latency_ms: 1500,
                  avg_tps: 12,
                },
              ],
            },
          ],
          channels: [
            {
              channel_index: 1,
              current_available: true,
              availability_rate: 100,
              avg_latency_ms: 1200,
              avg_tps: 12,
            },
            {
              channel_index: 2,
              current_available: false,
              availability_rate: 0,
              avg_latency_ms: 0,
              avg_tps: 0,
            },
          ],
        },
      })
      const user = userEvent.setup()
      render(
        <ThemeProvider defaultTheme='light'>
          <QueryClientProvider client={queryClient}>
            <ModelDetailsContent
              model={model}
              groupRatio={{ default: 1 }}
              usableGroup={{ default: { desc: '', ratio: 1 } }}
              endpointMap={{}}
              autoGroups={[]}
              priceRate={1}
              usdExchangeRate={1}
              tokenUnit='M'
            />
          </QueryClientProvider>
        </ThemeProvider>
      )
      expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute(
        'aria-selected',
        'true'
      )
      expect(
        screen.getByRole('table', { name: 'Channel availability' })
      ).toBeVisible()
      expect(screen.getByText('Channel 1')).toBeVisible()
      expect(screen.getByText('Channel 2')).toBeVisible()
      expect(
        screen.getByText(
          'Channel availability history includes real requests and health checks from the last 24 hours.'
        )
      ).toBeVisible()
      const channelTable = screen.getByRole('table', {
        name: 'Channel availability',
      })
      expect(within(channelTable).getAllByRole('row')).toHaveLength(3)
      expect(
        within(channelTable).queryByRole('columnheader', { name: 'Group' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('table', { name: 'Historical performance by group' })
      ).not.toBeInTheDocument()
      expect(screen.getByText('82.00%')).toBeVisible()
      const overallStatus = screen.getAllByRole('status', {
        name: 'Current status',
      })[0]
      expect(screen.getByText('2.10s')).toBeVisible()
      expect(screen.getByText('23.0 t/s')).toBeVisible()
      expect(overallStatus).toHaveTextContent(status)
      expect(overallStatus).toHaveClass(color)
      expect(overallStatus).toHaveAttribute(
        'title',
        expect.stringContaining('Last observed:')
      )
      expect(
        screen.getByRole('region', { name: 'Availability (last 24h)' })
      ).toBeVisible()
      await user.click(screen.getByRole('tab', { name: 'Performance' }))
      expect(
        within(
          screen.getByRole('group', { name: 'Average latency' })
        ).getByText('2.10s')
      ).toBeVisible()
      expect(
        within(screen.getByRole('group', { name: 'TPS' })).getByText('23.0 t/s')
      ).toBeVisible()
      expect(
        screen.getAllByRole('status', { name: 'Current status' })[0]
      ).toHaveTextContent(status)
      expect(
        screen.getByRole('table', { name: 'Channel availability' })
      ).toBeVisible()
    }
  )

  it('shows current model and channel states independently of their historical rates', () => {
    renderPerformance({
      model_name: model.model_name,
      groups: [group()],
      current_available: true,
      availability_rate: 0,
      availability_series: [{ ts: 1788782400, success_rate: 0 }],
      channels: [
        {
          channel_index: 1,
          current_available: true,
          availability_rate: 0,
          success_rate: 50,
          avg_latency_ms: 1200,
          avg_tps: 12,
          series: [{ ts: 1788782400, success_rate: 0 }],
        },
        {
          channel_index: 2,
          current_available: false,
          availability_rate: 100,
          avg_latency_ms: 2000,
          avg_tps: 0,
          series: [{ ts: 1788782400, success_rate: 100 }],
        },
      ],
    })
    const table = screen.getByRole('table', { name: 'Channel availability' })
    expect(
      within(table).queryByRole('columnheader', { name: 'Group' })
    ).not.toBeInTheDocument()
    expect(within(table).getAllByRole('row')).toHaveLength(3)
    const first = within(table).getByRole('row', { name: /Channel 1/ })
    const second = within(table).getByRole('row', { name: /Channel 2/ })
    expect(within(first).getByText('0.00%')).toBeVisible()
    expect(within(second).getByText('100.00%')).toBeVisible()
    const firstStatus = within(first).getByRole('status', {
      name: 'Current status',
    })
    const secondStatus = within(second).getByRole('status', {
      name: 'Current status',
    })
    expect(firstStatus).toHaveTextContent('Currently available')
    expect(firstStatus).toHaveClass('text-success')
    expect(secondStatus).toHaveTextContent('Currently unavailable')
    expect(secondStatus).toHaveClass('text-destructive')
    expect(within(first).getByText('1.20s')).toBeVisible()
    expect(
      within(first).getByRole('img', { name: 'Channel 1 availability samples' })
    ).toBeVisible()
    expect(
      within(second).getByRole('img', {
        name: 'Channel 2 availability samples',
      })
    ).toBeVisible()
    const overall = screen.getByRole('group', {
      name: 'Availability (last 24h)',
    })
    expect(within(overall).getByText('0.00%')).toBeVisible()
    expect(
      screen.getAllByRole('status', { name: 'Current status' })[0]
    ).toHaveTextContent('Currently available')
  })

  it('keeps unprobed channels visible without treating legacy success as availability', () => {
    renderPerformance({
      model_name: model.model_name,
      groups: [group(100)],
      channels: [
        {
          channel_index: 1,
          success_rate: 100,
          avg_latency_ms: 0,
          avg_tps: 0,
          series: null,
        },
      ],
    })
    const row = screen.getByRole('row', { name: /Channel 1/ })
    expect(within(row).getAllByText('Not monitored')).toHaveLength(3)
    expect(
      within(row).getByRole('status', { name: 'Current status' })
    ).toHaveTextContent('Not monitored')
    expect(within(row).queryByText(/100/)).not.toBeInTheDocument()
    const overall = screen.getByRole('group', {
      name: 'Availability (last 24h)',
    })
    expect(within(overall).getByText('Not monitored')).toBeVisible()
  })

  it.each([82, 100, 0])(
    'uses the unified history timeline and reports availability %s without a separate group-success table',
    async (availability) => {
      const legacy = {
        ...group(84),
        series: [
          {
            ts: 1788782400,
            success_rate: 84,
            avg_ttft_ms: 500,
            avg_latency_ms: 1500,
            avg_tps: 12,
          },
        ],
      }
      renderPerformance({
        model_name: model.model_name,
        groups: [legacy, { ...group(0), group: 'new-only' }],
        availability_rate: availability,
        availability_series: [
          { ts: 1788778800, success_rate: 72 },
          { ts: 1788782400, success_rate: availability },
        ],
        channels: [],
      })
      expect(
        screen.queryByRole('table', { name: 'Historical performance by group' })
      ).not.toBeInTheDocument()
      expect(screen.queryByText('84.0%')).not.toBeInTheDocument()
      const overall = screen.getByRole('group', {
        name: 'Availability (last 24h)',
      })
      expect(
        within(overall).getByText(`${availability.toFixed(2)}%`)
      ).toBeVisible()
      const timeline = screen.getByRole('region', {
        name: 'Availability (last 24h)',
      })
      expect(timeline).toBeVisible()
      expect(
        within(timeline).getByText(
          'Real requests and health checks both contribute to availability history. Current availability uses recent results from enabled channels; one successful channel is enough.'
        )
      ).toBeVisible()
      expect(
        within(timeline).queryByText('No uptime data available')
      ).not.toBeInTheDocument()
      await waitFor(() =>
        expect(
          renderChart.mock.calls.map(([props]) => props.spec)
        ).toContainEqual(
          expect.objectContaining({
            data: [
              expect.objectContaining({
                values: [
                  expect.objectContaining({ uptime: 72 }),
                  expect.objectContaining({ uptime: availability }),
                ],
              }),
            ],
          })
        )
      )
      expect(
        screen.getByRole('table', { name: 'Channel availability' })
      ).toBeVisible()
    }
  )

  it.each([
    { current_available: true, status: 'Currently available' },
    { current_available: false, status: 'Currently unavailable' },
  ])(
    'shows $status before historical metrics are available',
    ({ current_available, status }) => {
      renderPerformance({
        model_name: model.model_name,
        groups: [],
        current_available,
      })
      expect(
        screen.getByRole('status', { name: 'Current status' })
      ).toHaveTextContent(status)
      expect(
        screen.queryByText(
          'Performance data is not yet available for this model.'
        )
      ).not.toBeInTheDocument()
    }
  )

  it('keeps an expired model observation visible before historical metrics exist', () => {
    renderPerformance({
      model_name: model.model_name,
      groups: [],
      current_observed_at: 1788782400,
    })
    expect(
      screen.getByRole('status', { name: 'Current status' })
    ).toHaveTextContent('Waiting for update')
    expect(
      screen.getByRole('status', { name: 'Current status' })
    ).toHaveAttribute('title', expect.stringContaining('Last observed:'))
    expect(
      screen.queryByText(
        'Performance data is not yet available for this model.'
      )
    ).not.toBeInTheDocument()
  })

  it('shows an expired channel observation as waiting while preserving its history', () => {
    renderPerformance({
      model_name: model.model_name,
      groups: [],
      channels: [
        {
          channel_index: 1,
          current_observed_at: 1788782400,
          availability_rate: 100,
          avg_latency_ms: 1000,
          avg_tps: 20,
        },
      ],
    })
    const row = screen.getByRole('row', { name: /Channel 1/ })
    expect(
      within(row).getByRole('status', { name: 'Current status' })
    ).toHaveTextContent('Waiting for update')
    expect(
      within(row).getByRole('status', { name: 'Current status' })
    ).toHaveAttribute('title', expect.stringContaining('Last observed:'))
    expect(within(row).getByText('100.00%')).toBeVisible()
  })

  it.each([undefined, 0])(
    'leaves totals without measurements blank instead of averaging group values (%s)',
    (value) => {
      renderPerformance({
        model_name: model.model_name,
        groups: [
          group(),
          { ...group(), group: 'empty', avg_latency_ms: 0, avg_tps: 0 },
        ],
        avg_latency_ms: value,
        avg_tps: value,
      })
      expect(
        within(
          screen.getByRole('group', { name: 'Average latency' })
        ).getByText('—')
      ).toBeVisible()
      expect(
        within(screen.getByRole('group', { name: 'TPS' })).getByText('—')
      ).toBeVisible()
    }
  )

  it('shows a retry action when loading fails and recovers to the empty monitoring state', async () => {
    vi.spyOn(api, 'get')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { model_name: model.model_name, groups: [], channels: [] },
        },
      })
    const user = userEvent.setup()
    renderPerformance()
    expect(screen.getByText('Loading...')).toBeVisible()
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Not monitored')).toBeVisible()
    expect(
      screen.getByText('Performance data is not yet available for this model.')
    ).toBeVisible()
  })
})

const adminChannels = [
  {
    channel_id: 20,
    channel_name: 'Primary Provider',
    status: 1,
    current_available: true,
    current_observed_at: 1788782400,
    availability_rate: 9.52,
    keys: [
      {
        key_index: 0,
        key_hint: 'sk-***a1b2',
        key: 'should-never-be-rendered',
        enabled: true,
        current_available: false,
        observed_at: 1788782400,
        success: false,
        status_code: 402,
        error: 'Insufficient balance',
      },
      {
        key_index: 1,
        key_hint: 'sk-***c3d4',
        enabled: true,
        current_available: true,
        observed_at: 1788782400,
        success: true,
        status_code: 200,
      },
      { key_index: 2, key_hint: '****', enabled: false, observed_at: 0 },
      {
        key_index: 3,
        key_hint: 'sk-***e5f6',
        enabled: true,
        observed_at: 1788778800,
        success: false,
        status_code: 0,
      },
    ],
  },
  {
    channel_id: 42,
    channel_name: 'Fallback Provider',
    status: 1,
    current_available: false,
    current_observed_at: 1788782400,
    availability_rate: 50,
    keys: [],
  },
]

function renderChannelAvailability() {
  return render(
    <QueryClientProvider client={queryClient}>
      <ModelChannelAvailability
        modelName={model.model_name}
        channels={[
          {
            channel_index: 1,
            current_available: true,
            availability_rate: 9.52,
            avg_latency_ms: 1000,
            avg_tps: 20,
          },
        ]}
      />
    </QueryClientProvider>
  )
}

describe('admin channel key diagnostics', () => {
  it.each([null, { id: 2, username: 'member', role: 1 }])(
    'keeps anonymous channel data without requesting private diagnostics for %j',
    (user) => {
      useAuthStore.getState().auth.setUser(user)
      const get = vi.spyOn(api, 'get')
      renderChannelAvailability()
      expect(screen.getByText('Channel 1')).toBeVisible()
      expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
      expect(screen.queryByText('Masked key')).not.toBeInTheDocument()
      expect(get).not.toHaveBeenCalled()
    }
  )

  it('expands masked keys and preserves each key result, error and missing observation', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { model_name: model.model_name, channels: adminChannels },
      },
    })
    const user = userEvent.setup()
    renderChannelAvailability()
    const trigger = await screen.findByRole('button', {
      name: /Primary Provider/,
    })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(within(trigger).getByText('9.52%')).toBeVisible()
    expect(within(trigger).getByText('Currently available')).toBeVisible()
    await user.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(
      screen.queryByText('should-never-be-rendered')
    ).not.toBeInTheDocument()
    const table = screen.getByRole('table', {
      name: 'Key availability for Primary Provider',
    })
    const failed = within(table).getByRole('row', { name: /sk-\*\*\*a1b2/ })
    expect(within(failed).getByText('#1')).toBeVisible()
    expect(within(failed).getByText('Currently unavailable')).toBeVisible()
    expect(within(failed).getByText('402')).toBeVisible()
    expect(within(failed).getByText('Insufficient balance')).toBeVisible()
    await user.click(
      within(failed).getByRole('button', { name: 'Copy error details' })
    )
    expect(await navigator.clipboard.readText()).toBe('Insufficient balance')
    const healthy = within(table).getByRole('row', { name: /sk-\*\*\*c3d4/ })
    expect(within(healthy).getByText('Currently available')).toBeVisible()
    expect(
      within(healthy).queryByText('Insufficient balance')
    ).not.toBeInTheDocument()
    const unknown = within(table).getByRole('row', { name: /#3/ })
    expect(within(unknown).getByText('Disabled')).toBeVisible()
    expect(
      within(unknown).getByRole('status', { name: 'Current status' })
    ).toHaveTextContent('Not monitored')
    expect(within(unknown).queryByText(/1970/)).not.toBeInTheDocument()
    const staleFailure = within(table).getByRole('row', {
      name: /sk-\*\*\*e5f6/,
    })
    expect(
      within(staleFailure).getByText('No error details available')
    ).toBeVisible()
    expect(
      within(staleFailure).getByRole('status', { name: 'Current status' })
    ).toHaveTextContent('Waiting for update')
    expect(
      within(staleFailure).getByRole('status', { name: 'Current status' })
    ).toHaveAttribute('title', expect.stringContaining('Last observed:'))
    await user.click(screen.getByRole('button', { name: /Fallback Provider/ }))
    expect(screen.getByText('No keys found')).toBeVisible()
    expect(get).toHaveBeenCalledWith(
      '/api/perf-metrics/admin',
      expect.objectContaining({
        params: { model: model.model_name, hours: 24 },
        disableDuplicate: true,
        signal: expect.any(AbortSignal),
      })
    )
    trigger.focus()
    await user.keyboard('{Enter}')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await user.keyboard('{Enter}')
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
  })

  it('removes private diagnostics immediately when an admin becomes a regular user', async () => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
    const get = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { model_name: model.model_name, channels: adminChannels },
      },
    })
    renderChannelAvailability()
    await screen.findByRole('button', { name: /Primary Provider/ })
    await act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ id: 1, username: 'member', role: 1 })
    )
    expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
    expect(screen.getByText('Channel 1')).toBeVisible()
    expect(get).toHaveBeenCalledTimes(1)
    await waitFor(() =>
      expect(
        queryClient
          .getQueryCache()
          .findAll({ queryKey: ['perf-metrics-admin'] })
      ).toHaveLength(0)
    )
  })

  it('does not reuse the previous admin account data while the next account loads', async () => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'admin-a', role: 10 })
    let complete: (value: unknown) => void = () => undefined
    const pending = new Promise((resolve) => {
      complete = resolve
    })
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: {
          success: true,
          data: { model_name: model.model_name, channels: adminChannels },
        },
      })
      .mockReturnValueOnce(pending as never)
    renderChannelAvailability()
    await screen.findByRole('button', { name: /Primary Provider/ })
    await act(() =>
      useAuthStore
        .getState()
        .auth.setUser({ id: 2, username: 'admin-b', role: 10 })
    )
    expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
    expect(screen.getByText('Loading...')).toBeVisible()
    await act(() =>
      complete({
        data: {
          success: true,
          data: { model_name: model.model_name, channels: [] },
        },
      })
    )
    expect(await screen.findByText('Not monitored')).toBeVisible()
    expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
  })
})

it('keeps admin channel diagnostics available before public historical metrics exist', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { model_name: model.model_name, channels: adminChannels },
    },
  })
  renderPerformance({ model_name: model.model_name, groups: [], channels: [] })
  expect(
    await screen.findByRole('button', { name: /Primary Provider/ })
  ).toBeVisible()
})

it('cancels in-flight private diagnostics and discards their result after sign-out', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  let complete: (value: unknown) => void = () => undefined
  let requestSignal: { aborted: boolean } | undefined
  const pending = new Promise((resolve) => {
    complete = resolve
  })
  const get = vi.spyOn(api, 'get').mockImplementation((_path, config) => {
    requestSignal = config?.signal
    return pending as never
  })
  renderChannelAvailability()
  await waitFor(() => expect(get).toHaveBeenCalledOnce())
  await act(() => useAuthStore.getState().auth.reset())
  expect(requestSignal?.aborted).toBe(true)
  expect(screen.getByText('Channel 1')).toBeVisible()
  await act(() =>
    complete({
      data: {
        success: true,
        data: { model_name: model.model_name, channels: adminChannels },
      },
    })
  )
  expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
  await waitFor(() =>
    expect(
      queryClient.getQueryCache().findAll({ queryKey: ['perf-metrics-admin'] })
    ).toHaveLength(0)
  )
})

it('shows a retry action after an admin diagnostics error without displaying private stale results', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 10 })
  vi.spyOn(api, 'get')
    .mockRejectedValueOnce(new Error('Unavailable'))
    .mockResolvedValueOnce({
      data: {
        success: true,
        data: { model_name: model.model_name, channels: adminChannels },
      },
    })
  const user = userEvent.setup()
  renderChannelAvailability()
  expect(screen.queryByText('Primary Provider')).not.toBeInTheDocument()
  await user.click(await screen.findByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByRole('button', { name: /Primary Provider/ })
  ).toBeVisible()
})
