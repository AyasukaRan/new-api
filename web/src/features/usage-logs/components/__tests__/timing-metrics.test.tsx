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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { usageLogSchema } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'
import { LogMetricLegend } from '../log-metric-legend'
import { LogTimingDetails } from '../log-timing-details'
import { StreamModeCell, TimingMetricsCell } from '../timing-metrics-cell'
import { TokenPerformanceCell } from '../token-performance-cell'

const messagesStream: LogOtherData = {
  request_path: '/v1/messages?beta=true',
  stream_status: {
    status: 'ok',
    end_reason: 'eof',
    response_status: 'completed',
  },
}

test.each([
  [true, 'Identified from the Messages endpoint and recorded stream output.'],
  [false, 'Identified from recorded stream output.'],
] as const)(
  'explains inferred SSE on focus, respecting endpoint visibility (%s)',
  async (showEndpoint, explanation) => {
    render(
      <StreamModeCell
        logType={2}
        isStream
        metadata={messagesStream}
        showEndpoint={showEndpoint}
      />
    )
    expect(screen.getByText('SSE')).toBeVisible()
    await userEvent.tab()
    expect(await screen.findByRole('tooltip')).toHaveTextContent(explanation)
  }
)

test.each([
  [
    'observed first token',
    2,
    { stream_status: { status: 'ok', end_reason: 'eof' }, frt: 51 },
    'SSE',
  ],
  [
    'empty EOF',
    2,
    { stream_status: { status: 'ok', end_reason: 'eof' }, frt: -1000 },
    'Protocol not recorded',
  ],
  ['failed request', 5, {}, 'Protocol not recorded'],
  [
    'early disconnect',
    2,
    { stream_status: { status: 'error', end_reason: 'client_gone' }, frt: 25 },
    'Protocol not recorded',
  ],
  [
    'soft stop',
    2,
    { stream_status: { status: 'ok', end_reason: 'handler_stop' }, frt: 25 },
    'Protocol not recorded',
  ],
  [
    'incomplete response',
    2,
    {
      stream_status: {
        status: 'ok',
        end_reason: 'eof',
        response_status: 'incomplete',
      },
      frt: 25,
    },
    'Protocol not recorded',
  ],
  [
    'different Messages path',
    2,
    { request_path: '/v1/messages/count_tokens' },
    'Protocol not recorded',
  ],
  [
    'ambiguous Responses path',
    2,
    { request_path: '/v1/responses' },
    'Protocol not recorded',
  ],
  ['legacy WebSocket evidence', 2, { ws: true }, 'WS'],
  ['recorded HTTP before inference', 2, { response_transport: 'http' }, 'HTTP'],
  ['negotiated WebSocket on error', 5, { response_transport: 'ws' }, 'WS'],
  ['async task', 2, { is_task: true }, 'Async'],
] satisfies [string, number, Partial<LogOtherData>, string][])(
  'renders transport for %s',
  (_, logType, patch, expected) => {
    render(
      <StreamModeCell
        logType={logType}
        isStream
        metadata={{ ...messagesStream, ...patch }}
        showEndpoint
      />
    )
    expect(screen.getByText(expected)).toBeVisible()
  }
)

test('distinguishes prepared SSE headers on an interrupted attempt without asserting delivery or disclosing the path', async () => {
  render(
    <StreamModeCell
      logType={5}
      isStream
      showEndpoint={false}
      metadata={{
        request_path: '/v1/messages',
        prepared_response_transport: 'sse',
        stream_status: { status: 'error', end_reason: 'client_gone' },
      }}
    />
  )
  expect(screen.getByText('SSE')).toBeVisible()
  expect(screen.getByLabelText('Stream Status')).toBeVisible()
  await userEvent.tab()
  expect(await screen.findByRole('tooltip')).toHaveTextContent(
    'The gateway prepared SSE headers. No response body was recorded, so delivery to the client could not be confirmed.'
  )
  expect(screen.queryByText('/v1/messages')).not.toBeInTheDocument()
})

test('uses the observed transport or confirmed stream output ahead of prepared headers', async () => {
  const props = { logType: 2, isStream: true, showEndpoint: true }
  const prepared = {
    ...messagesStream,
    prepared_response_transport: 'sse' as const,
  }
  const { rerender } = render(
    <StreamModeCell
      {...props}
      metadata={{ ...prepared, response_transport: 'http' }}
    />
  )
  expect(screen.getByText('HTTP')).toBeVisible()
  expect(screen.queryByText('SSE')).not.toBeInTheDocument()
  rerender(<StreamModeCell {...props} metadata={prepared} />)
  await userEvent.tab()
  expect(await screen.findByRole('tooltip')).toHaveTextContent(
    'Identified from the Messages endpoint and recorded stream output.'
  )
})

test('explains a missing protocol without calling an unknown stream a failure', async () => {
  render(<StreamModeCell logType={2} isStream metadata={null} showEndpoint />)
  expect(screen.getByText('Protocol not recorded')).toBeVisible()
  await userEvent.tab()
  expect(await screen.findByRole('tooltip')).toHaveTextContent(
    'The log records streaming mode but does not identify the response protocol.'
  )
  expect(screen.queryByLabelText('Stream Status')).not.toBeInTheDocument()
})

test.each([
  [1, '1ms'],
  [437, '437ms'],
  [999, '999ms'],
  [1000, '1.0s'],
  [1450, '1.4s'],
])(
  'formats a %i ms first token as %s without changing total duration',
  (frtMs, label) => {
    render(
      <TimingMetricsCell
        useTimeSec={14}
        completionTokens={200}
        frtMs={frtMs}
        isStream
      />
    )
    expect(screen.getByText(label)).toBeVisible()
    expect(screen.getByText('14.0s')).toBeVisible()
  }
)

test.each([undefined, 0])(
  'keeps an unavailable first-token value (%s) as N/A',
  (frtMs) => {
    render(
      <TimingMetricsCell
        useTimeSec={14}
        completionTokens={200}
        frtMs={frtMs}
        isStream
        indicator='dot'
        compact
      />
    )
    expect(screen.getByText('N/A')).toBeVisible()
  }
)

test('does not show first-token timing for non-stream requests', () => {
  render(
    <TimingMetricsCell
      useTimeSec={14}
      completionTokens={200}
      frtMs={437}
      isStream={false}
    />
  )
  expect(screen.queryByText('First token')).not.toBeInTheDocument()
  expect(screen.getByText('14 t/s')).toBeVisible()
})

test.each([
  [0, 200],
  [14, 0],
])(
  'omits speed when duration or output tokens are unavailable',
  (useTimeSec, completionTokens) => {
    render(
      <TimingMetricsCell
        useTimeSec={useTimeSec}
        completionTokens={completionTokens}
        frtMs={437}
        isStream
      />
    )
    expect(screen.getByText('437ms')).toBeVisible()
    expect(screen.queryByText(/t\/s$/)).not.toBeInTheDocument()
  }
)

test.each([
  [2, 14],
  [5, 0],
])(
  'keeps a recorded first-token time in type %i log details even when duration is %is',
  async (type, useTime) => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const updatedAt = Date.now() + 60_000
    client.setQueryData(['status'], {}, { updatedAt })
    client.setQueryData(['pricing'], { data: [], vendors: [] }, { updatedAt })
    try {
      render(
        <QueryClientProvider client={client}>
          <DetailsDialog
            open
            isAdmin={false}
            isRoot={false}
            onOpenChange={() => undefined}
            log={usageLogSchema.parse({
              id: 1,
              user_id: 1,
              created_at: 1,
              type,
              content: '',
              model_name: 'deepseek-flash',
              use_time: useTime,
              is_stream: true,
              other: JSON.stringify({ frt: 437 }),
            })}
          />
        </QueryClientProvider>
      )
      expect(await screen.findByText('(FRT: 437ms)')).toBeVisible()
    } finally {
      client.clear()
    }
  }
)

test.each([true, false])(
  'timing details load upstream headers on demand for the admin view only (%s)',
  async (admin) => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const auth = useAuthStore.getState().auth
    const previousUser = auth.user
    auth.setUser({ id: 1, username: 'timing-user', role: admin ? 10 : 1 })
    const load = vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          legs: [
            {
              direction: 'upstream_response',
              headers: { 'OpenAI-Processing-Ms': ['893'] },
            },
            { direction: 'upstream_request' },
            {
              direction: 'upstream_response',
              headers: {
                'OpenAI-Processing-Ms': ['372'],
                'x-envoy-upstream-service-time': ['-1'],
              },
            },
          ],
        },
      },
    })
    const log = usageLogSchema.parse({
      id: 1,
      user_id: 1,
      created_at: 1,
      type: 2,
      content: '',
      use_time: 1,
      prompt_tokens: 50,
      completion_tokens: 8,
      is_stream: true,
    })
    try {
      render(
        <QueryClientProvider client={queryClient}>
          <TokenPerformanceCell
            log={log}
            other={{
              frt: 400,
              cache_tokens: 50,
              cache_write_tokens: 0,
              usage_semantic: 'anthropic',
              admin_info: { trace_id: 'only-this-request' },
            }}
            isAdmin={admin}
          />
        </QueryClientProvider>
      )
      expect(screen.getByText('100')).toBeVisible()
      expect(screen.getByRole('progressbar')).toHaveAttribute(
        'aria-valuenow',
        '50'
      )
      expect(load).not.toHaveBeenCalled()
      await userEvent.click(
        screen.getByRole('button', { name: /Timing breakdown/ })
      )
      const breakdown = within(
        await screen.findByRole('tabpanel', { name: 'Timing breakdown' })
      )
      expect(breakdown.getByText('400ms')).toBeVisible()
      expect(breakdown.getByText('<1.6s')).toBeVisible()
      if (admin) {
        expect(await breakdown.findByText('372ms')).toBeVisible()
        expect(breakdown.queryByText('893ms')).not.toBeInTheDocument()
        expect(
          breakdown.queryByText('Upstream proxy service time')
        ).not.toBeInTheDocument()
        expect(load).toHaveBeenCalledTimes(1)
      } else {
        expect(load).not.toHaveBeenCalled()
        expect(
          breakdown.queryByText('Upstream processing time')
        ).not.toBeInTheDocument()
      }
    } finally {
      queryClient.clear()
      load.mockRestore()
      auth.setUser(previousUser)
    }
  }
)

test('keeps a recorded first token when rounded duration is shorter, without inventing a negative phase', async () => {
  const log = usageLogSchema.parse({
    id: 2,
    user_id: 1,
    created_at: 1,
    type: 5,
    content: '',
    use_time: 0,
    is_stream: true,
  })
  render(<TokenPerformanceCell log={log} other={{ frt: 633 }} />)
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: /Timing breakdown/ })
  )
  const breakdown = within(
    await screen.findByRole('tabpanel', { name: 'Timing breakdown' })
  )
  expect(breakdown.getByText('633ms')).toBeVisible()
  expect(breakdown.getByText('<367ms')).toBeVisible()
})

test('table metric icons stay beside short values and all groups size to their content', () => {
  const log = usageLogSchema.parse({
    id: 4,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    use_time: 2,
    prompt_tokens: 10,
    completion_tokens: 1,
  })
  render(<TokenPerformanceCell log={log} other={{ cache_tokens: 0 }} />)

  for (const name of ['Output Tokens', 'Cache read tokens', 'Total time']) {
    const group = screen.getByRole('term', { name }).closest('dl')
    expect(group).toHaveClass(
      'grid-cols-[max-content_max-content]',
      'justify-start'
    )
    expect(group?.parentElement).toHaveClass('w-max', 'max-w-full')
  }
})

test('a page-metered task shows the page count and duration without empty token metrics', () => {
  const log = usageLogSchema.parse({
    id: 3,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    use_time: 4,
  })
  render(
    <TokenPerformanceCell
      log={log}
      other={{
        is_task: true,
        cache_tokens: 0,
        metered_usage: { unit: 'page', count: 3 },
      }}
    />
  )
  expect(screen.getByText('3 page')).toBeVisible()
  expect(screen.getByText('4.0s')).toBeVisible()
  for (const label of [
    'Total input tokens',
    'Output Tokens',
    'Cache read tokens',
    'Cache write tokens',
    'Cache hit rate',
    'Time to first token',
    'Output speed',
  ]) {
    expect(screen.queryByRole('term', { name: label })).not.toBeInTheDocument()
  }
})

test.each([true, false])(
  'metric icons have one keyboard-accessible guide for the current view (admin: %s)',
  async (isAdmin) => {
    render(<LogMetricLegend isAdmin={isAdmin} />)
    const trigger = screen.getByRole('button', { name: 'Metric icons' })
    await userEvent.tab()
    expect(trigger).toHaveFocus()
    await userEvent.keyboard('{Enter}')
    const guide = within(
      await screen.findByRole('dialog', { name: 'Metric icons' })
    )
    expect(guide.getByText('Cache read tokens')).toBeVisible()
    expect(guide.getByText('Cache write tokens')).toBeVisible()
    expect(guide.getByText('Free allowance deduction')).toBeVisible()
    for (const name of ['Upstream cost', 'Gross profit']) {
      if (isAdmin) expect(guide.getByText(name)).toBeVisible()
      else expect(guide.queryByText(name)).not.toBeInTheDocument()
    }
    await userEvent.keyboard('{Escape}')
    expect(trigger).toHaveFocus()
  }
)

test('uses the measured elapsed milliseconds for fast requests and preserves an observed submillisecond first token', () => {
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    is_stream: true,
    use_time: 0,
    completion_tokens: 40,
  })
  render(
    <TokenPerformanceCell log={log} other={{ frt: 0, duration_ms: 250 }} />
  )
  expect(screen.getByText('0ms')).toBeVisible()
  expect(screen.getByText('250ms')).toBeVisible()
  expect(screen.getByText('160 t/s')).toBeVisible()
})

test('separates exact gateway phases from independently measured routing attempts', () => {
  const log = usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    is_stream: true,
    use_time: 5,
  })
  const event = (action: string, attempt: number, elapsed_ms: number) => ({
    decision: { action, reason: '', source: '' },
    attempt,
    elapsed_ms,
  })
  const other: LogOtherData = {
    frt: 545,
    duration_ms: 4340,
    admin_info: {
      request_policy: [
        event('attempt', 1, 12),
        event('failure', 1, 180),
        event('retry', 1, 180),
        event('attempt', 2, 184),
        event('success', 2, 4200),
      ],
    },
  }
  const { rerender } = render(
    <LogTimingDetails log={log} other={other} isAdmin />
  )
  expect(screen.getByText('4.34s')).toBeVisible()
  expect(screen.getByText('545ms')).toBeVisible()
  expect(screen.getByText('3.795s')).toBeVisible()
  expect(screen.getByText('12ms')).toBeVisible()
  expect(screen.getByText('168ms')).toBeVisible()
  expect(screen.getByText('4ms')).toBeVisible()
  expect(screen.getByText('4.016s')).toBeVisible()
  rerender(<LogTimingDetails log={log} other={other} isAdmin={false} />)
  expect(screen.queryByText('Routing observations')).not.toBeInTheDocument()
})

test.each([
  {
    name: 'Responses reports cache and reasoning as subsets',
    openWith: 'hover',
    prompt: 1000,
    percentOfOutput: 100 / 1100,
    other: {
      cache_tokens: 200,
      cache_write_tokens: 150,
      cache_creation_tokens_12h: 50,
      reasoning_tokens: 35,
    },
    details: ['650', '100', '200', '150', '50', '35', '1,100'],
  },
  {
    name: 'Anthropic reports ordinary input separately',
    openWith: 'keyboard',
    prompt: 650,
    percentOfOutput: 100 / 1100,
    other: {
      usage_semantic: 'anthropic',
      cache_tokens: 200,
      cache_creation_tokens_5m: 100,
      cache_creation_tokens_1h: 50,
    },
    details: ['650', '100', '200', '150', '100', '50', 'Not recorded', '1,100'],
  },
  {
    name: 'recorded zeros and missing facts remain distinct',
    openWith: 'touch',
    prompt: 50,
    percentOfOutput: 100 / 150,
    other: { cache_tokens: 0, cache_ratio: 0.1, reasoning_tokens: 0 },
    details: ['50', '100', '0', 'Not recorded', '0', '150'],
  },
  {
    name: 'legacy totals do not prove a cache or reasoning breakdown',
    openWith: 'click',
    prompt: 50,
    percentOfOutput: 100 / 150,
    other: {},
    details: [
      'Not recorded',
      '100',
      'Not recorded',
      'Not recorded',
      'Not recorded',
      '150',
    ],
  },
  {
    name: 'historical separate cache input lacks a reliable grand total',
    openWith: 'click',
    prompt: 50,
    other: { usage_semantic: 'anthropic', cache_tokens: 25 },
    percentOfOutput: null,
    details: [
      '50',
      '100',
      '25',
      'Not recorded',
      'Not recorded',
      'Not recorded',
    ],
  },
  {
    name: 'all known counts are zero',
    openWith: 'click',
    prompt: 0,
    output: 0,
    other: { cache_tokens: 0, cache_write_tokens: 0 },
    percentOfOutput: null,
    details: ['0', '0', '0', '0', 'Not recorded', '0'],
  },
  {
    name: 'contradictory subsets keep their original counts without plotting',
    openWith: 'click',
    prompt: 10,
    other: { cache_tokens: 8, cache_write_tokens: 6 },
    percentOfOutput: null,
    details: ['Not recorded', '100', '8', '6', 'Not recorded', '110'],
  },
] satisfies {
  name: string
  prompt: number
  output?: number
  percentOfOutput: number | null
  other: LogOtherData
  details: string[]
  openWith: 'hover' | 'keyboard' | 'click' | 'touch'
}[])(
  'token breakdown: $name',
  async ({ prompt, output, other, details, openWith, percentOfOutput }) => {
    const log = usageLogSchema.parse({
      id: 1,
      user_id: 1,
      created_at: 1,
      type: 2,
      content: '',
      prompt_tokens: prompt,
      completion_tokens: output ?? 100,
    })
    render(<TokenPerformanceCell log={log} other={other} />)
    const trigger = screen.getByRole('button', {
      name:
        openWith === 'hover'
          ? 'Cache read tokens · Token Breakdown'
          : 'Token Breakdown',
    })
    if (openWith === 'hover') await userEvent.hover(trigger)
    else if (openWith === 'keyboard') {
      await userEvent.tab()
      expect(trigger).toHaveFocus()
      if (trigger.getAttribute('aria-expanded') !== 'true')
        await userEvent.keyboard('{Enter}')
    } else if (openWith === 'touch') {
      await userEvent.pointer([
        { keys: '[TouchA>]', target: trigger },
        { keys: '[/TouchA]' },
      ])
    } else await userEvent.click(trigger)
    const card = within(
      await screen.findByRole('dialog', { name: 'Token Breakdown' })
    )
    expect(card.getAllByRole('definition').map((dd) => dd.textContent)).toEqual(
      details
    )
    expect(card.getByText('(Included in output)')).toBeVisible()
    const chart = card.queryByRole('img', { name: 'Token distribution' })
    if (percentOfOutput == null) {
      expect(chart).not.toBeInTheDocument()
    } else {
      expect(chart).toBeVisible()
      const plotted = within(chart!)
      expect(
        parseFloat(plotted.getByTitle('Output Tokens').style.width)
      ).toBeCloseTo(100 * percentOfOutput)
      expect(plotted.queryByTitle('Reasoning tokens')).not.toBeInTheDocument()
      expect(
        plotted.queryByTitle('Cache Creation (12h)')
      ).not.toBeInTheDocument()
      if (other.reasoning_tokens === 35) {
        const reasoning = card.getByRole('img', {
          name: 'Reasoning tokens · Included in output',
        })
        expect(within(reasoning).getByTitle('Reasoning tokens')).toHaveStyle({
          width: '35%',
        })
      }
    }
  }
)
