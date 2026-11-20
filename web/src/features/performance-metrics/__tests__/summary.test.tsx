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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  GatewayModelHotspotsPanel,
  PerformanceHealthPanel,
  ResponsePerformancePanel,
} from '@/features/dashboard/components/overview/performance-health-panel'
import { ModelDetailsPerformance } from '@/features/pricing/components/model-details-performance'
import { UptimeSparkline } from '@/features/pricing/components/model-details-uptime-sparkline'

const chart = vi.hoisted(() =>
  vi.fn(
    (_props: {
      spec: { data: { id: string; values: Record<string, unknown>[] }[] }
    }) => null
  )
)
vi.mock('@visactor/react-vchart', () => ({ VChart: chart }))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: vi.fn() },
}))

let client: QueryClient
beforeEach(() => {
  chart.mockClear()
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
})
afterEach(() => client.clear())

const summary = { success_rate: 99.01, avg_latency_ms: 1009, avg_tps: 5 }
const windowStart = 1789387200
const windowEnd = windowStart + 23 * 3600 + 1800
const groups = [
  {
    group: 'a',
    success_rate: 100,
    avg_latency_ms: 1000,
    avg_ttft_ms: 100,
    avg_tps: 5,
    series: [
      {
        ts: windowStart,
        avg_ttft_ms: 100,
        avg_latency_ms: 1000,
        success_rate: 100,
        avg_tps: 5,
      },
    ],
  },
  {
    group: 'b',
    success_rate: 0,
    avg_latency_ms: 2000,
    avg_ttft_ms: 0,
    avg_tps: 0,
    series: [
      {
        ts: windowStart,
        avg_ttft_ms: 0,
        avg_latency_ms: 2000,
        success_rate: 0,
        avg_tps: 0,
      },
    ],
  },
]

function renderModelDetails() {
  client.setQueryData(['perf-metrics', 'test-model'], {
    success: true,
    data: {
      model_name: 'test-model',
      summary,
      current_available: true,
      current_observed_at: windowEnd,
      availability_rate: 100,
      availability_series: [{ ts: windowStart, success_rate: 100 }],
      channels: groups.map((group, index) => ({
        channel_index: index + 1,
        current_available: index === 0,
        current_observed_at: windowEnd,
        availability_rate: group.success_rate,
        avg_latency_ms: group.avg_latency_ms,
        avg_tps: group.avg_tps,
        series: group.series,
      })),
      series: [
        {
          ts: windowStart,
          avg_ttft_ms: 100,
          avg_latency_ms: 1009,
          success_rate: 99.01,
          avg_tps: 5,
        },
      ],
      window_start: windowStart,
      window_end: windowEnd,
      groups,
    },
  })
  return render(
    <QueryClientProvider client={client}>
      <ModelDetailsPerformance
        model={{
          id: 1,
          model_name: 'test-model',
          quota_type: 0,
          model_ratio: 1,
          completion_ratio: 1,
          enable_groups: ['a', 'b'],
        }}
      />
    </QueryClientProvider>
  )
}

describe('server performance summaries', () => {
  it('uses exact outcome counts and weighted timing for the monitored requests in the window', async () => {
    client.setQueryData(['perf-metrics-summary', 24, 'diagnostics'], {
      success: true,
      data: {
        summary: { success_rate: 100, avg_latency_ms: 250, avg_tps: 45 },
        diagnostics: {
          observed_from: windowStart + 3600,
          total: {
            p95_latency_ms: 2000,
            latency_sample_count: 1,
            peak_concurrency: 7,
            request_count: 101,
            success_count: 100,
            avg_ttft_ms: 120,
            avg_latency_ms: 1009,
            avg_tps: 5,
          },
          hours: [
            {
              ts: windowStart,
              avg_ttft_ms: 120,
              request_count: 100,
              success_count: 100,
              avg_latency_ms: 1000,
              avg_tps: 5,
            },
            {
              ts: windowStart + 3600,
              p95_latency_ms: 2000,
              latency_sample_count: 1,
              peak_concurrency: 7,
              request_count: 1,
              success_count: 0,
              avg_latency_ms: 2000,
              avg_tps: 0,
            },
          ],
          models: [],
        },
        models: [],
      },
    })
    render(
      <QueryClientProvider client={client}>
        <PerformanceHealthPanel />
        <ResponsePerformancePanel />
      </QueryClientProvider>
    )
    const health = within(
      screen.getByRole('region', { name: 'Request health' })
    )
    expect(health.getByText('101')).toBeVisible()
    expect(health.getByText('1')).toBeVisible()
    expect(health.getByText('99.01%')).toBeVisible()
    expect(
      health.getByRole('figure', { name: 'Hourly request outcomes' })
    ).toBeVisible()
    const performance = within(
      screen.getByRole('region', { name: 'Response performance' })
    )
    expect(performance.getByText('1.01s')).toBeVisible()
    expect(
      performance.getByRole('figure', {
        name: 'Hourly average and P95 latency',
      })
    ).toBeVisible()
    expect(performance.getByText('2.00s')).toBeVisible()
    expect(performance.getByText('P95 samples: 1')).toBeVisible()
    expect(performance.getByText(/^Earliest observation:/)).toBeVisible()
    expect(performance.getByText('120ms')).toBeVisible()
    await userEvent.click(
      performance.getByRole('button', { name: 'First token' })
    )
    expect(
      performance.getByRole('figure', { name: 'Hourly time to first token' })
    ).toBeVisible()
    await userEvent.click(
      performance.getByRole('button', { name: 'Throughput' })
    )
    expect(
      performance.getByRole('figure', { name: 'Hourly generation speed' })
    ).toBeVisible()
    await userEvent.click(
      performance.getByRole('button', { name: 'Concurrency' })
    )
    expect(performance.getByText('7')).toBeVisible()
    expect(
      performance.getByRole('figure', { name: 'Hourly peak concurrency' })
    ).toBeVisible()
    expect(performance.getByText(/single gateway instance/)).toBeVisible()
  })

  it('keeps a seven-day monitoring window distinct from the daily totals and preserves a measured zero TTFT', () => {
    const counts = {
      request_count: 126,
      success_count: 125,
      avg_latency_ms: 900,
      avg_ttft_ms: 0,
      avg_tps: 5,
    }
    client.setQueryData(['perf-metrics-summary', 168, 'diagnostics'], {
      success: true,
      data: {
        diagnostics: {
          total: counts,
          hours: [{ ts: windowStart, ...counts }],
          models: [{ model_name: 'weekly-model', ...counts }],
        },
        models: [],
      },
    })
    render(
      <QueryClientProvider client={client}>
        <PerformanceHealthPanel timelineOnly hours={168} />
        <GatewayModelHotspotsPanel hours={168} />
        <ResponsePerformancePanel hours={168} />
      </QueryClientProvider>
    )
    expect(
      screen.getAllByText('Monitored gateway requests · last 7 days')
    ).toHaveLength(3)
    const health = within(
      screen.getByRole('region', { name: 'Request health' })
    )
    expect(health.getByText('126')).toBeVisible()
    expect(
      health.queryByRole('button', { name: 'Models' })
    ).not.toBeInTheDocument()
    expect(
      within(
        screen.getByRole('region', { name: 'Response performance' })
      ).getByText('0ms')
    ).toBeVisible()
    expect(
      within(
        screen.getByRole('region', { name: 'Monitored model hotspots' })
      ).getByText('weekly-model')
    ).toBeVisible()
  })

  it('keeps historical metrics unknown, then shows a measured concurrency peak even without a completed request', async () => {
    const key = ['perf-metrics-summary', 24, 'diagnostics']
    const legacy = {
      request_count: 10,
      success_count: 10,
      avg_latency_ms: 750,
      avg_ttft_ms: null,
      avg_tps: 0,
    }
    client.setQueryData(key, {
      success: true,
      data: {
        diagnostics: {
          total: legacy,
          hours: [{ ...legacy, ts: windowStart }],
          models: [],
        },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <ResponsePerformancePanel />
      </QueryClientProvider>
    )
    expect(
      screen.getByText('P95 latency').nextElementSibling
    ).toHaveTextContent('—')
    expect(
      screen.getByText('Peak concurrency').nextElementSibling
    ).toHaveTextContent('—')
    await userEvent.click(screen.getByRole('button', { name: 'Concurrency' }))
    expect(screen.getByText('No data available')).toBeVisible()
    const telemetry = {
      request_count: 0,
      success_count: 0,
      avg_ttft_ms: null,
      avg_latency_ms: 0,
      avg_tps: 0,
      peak_concurrency: 3,
      p95_latency_ms: null,
      latency_sample_count: 0,
    }
    client.setQueryData(key, {
      success: true,
      data: {
        diagnostics: {
          total: telemetry,
          hours: [{ ...telemetry, ts: windowStart }],
          models: [],
        },
      },
    })
    expect(
      await screen.findByRole('figure', { name: 'Hourly peak concurrency' })
    ).toBeVisible()
    expect(
      screen.getByText('Peak concurrency').nextElementSibling
    ).toHaveTextContent('3')
    expect(
      screen.getByText('P95 latency').nextElementSibling
    ).toHaveTextContent('—')
  })

  it('keeps model availability distinct from weighted request success and uses weighted latency', () => {
    renderModelDetails()
    const availability = screen.getByRole('group', {
      name: 'Availability (last 24h)',
    })
    expect(within(availability).getByText('100.00%')).toBeVisible()
    expect(screen.queryByText('99.01%')).not.toBeInTheDocument()
    expect(screen.queryByText('50.00%')).not.toBeInTheDocument()
    expect(screen.getByText('1.01s')).toBeVisible()
  })

  it('feeds availability from channel-aware history and latency from the weighted backend series', async () => {
    renderModelDetails()
    await waitFor(() => expect(chart).toHaveBeenCalled())
    const specs = chart.mock.calls.map(([props]) => props.spec)
    const uptime = specs.find((value) => value.data[0].id === 'uptime')
    const uptimeValues = uptime?.data[0].values.map((value) => value.uptime)
    expect(uptimeValues).toContain(100)
    expect(uptimeValues).not.toContain(99.01)
    const latency = specs.find((value) => value.data[0].id === 'latency')
    expect(latency?.data[0].values.map((value) => value.ttft)).toEqual([100])
  })

  it('shows availability once per channel without exposing group names', () => {
    renderModelDetails()
    const channels = screen.getByRole('table', { name: 'Channel availability' })
    expect(within(channels).getByText('Channel 1')).toBeVisible()
    expect(within(channels).getByText('Channel 2')).toBeVisible()
    expect(within(channels).getByText('100.00%')).toBeVisible()
    expect(within(channels).getByText('0.00%')).toBeVisible()
    expect(within(channels).queryByText('a')).not.toBeInTheDocument()
    expect(within(channels).queryByText('b')).not.toBeInTheDocument()
  })

  it('displays the supplied group summary instead of averaging hourly percentages', () => {
    render(
      <UptimeSparkline
        overallSuccessRate={99.01}
        series={[
          {
            date: '2026-09-14T11:00:00Z',
            uptime_pct: 100,
            incidents: 0,
            outage_minutes: 0,
          },
          {
            date: '2026-09-14T12:00:00Z',
            uptime_pct: 0,
            incidents: 1,
            outage_minutes: 0,
          },
        ]}
      />
    )
    expect(screen.getByText('99.01%')).toBeVisible()
    expect(screen.queryByText('50.00%')).not.toBeInTheDocument()
  })

  it('keeps an empty summary unknown instead of inventing a zero success rate', () => {
    client.setQueryData(['perf-metrics-summary', 24, 'diagnostics'], {
      success: true,
      data: {
        models: [],
        summary: null,
        diagnostics: {
          total: {
            request_count: 0,
            success_count: 0,
            avg_ttft_ms: null,
            avg_latency_ms: 0,
            avg_tps: 0,
          },
          hours: [],
          models: [],
        },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <PerformanceHealthPanel />
      </QueryClientProvider>
    )
    expect(screen.getByText('—')).toBeVisible()
    expect(screen.getByText('No data available')).toBeVisible()
    expect(screen.queryByText('0.00%')).not.toBeInTheDocument()
  })
})
