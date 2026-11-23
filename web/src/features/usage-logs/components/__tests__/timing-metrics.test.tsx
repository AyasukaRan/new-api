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
import { DetailsDialog } from '../dialogs/details-dialog'
import { TimingMetricsCell } from '../timing-metrics-cell'
import { TokenPerformanceCell } from '../token-performance-cell'

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
        await screen.findByRole('dialog', { name: 'Timing breakdown' })
      )
      expect(breakdown.getByText('400ms')).toBeVisible()
      expect(breakdown.getByText('≈ 600ms')).toBeVisible()
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
    await screen.findByRole('dialog', { name: 'Timing breakdown' })
  )
  expect(breakdown.getByText('633ms')).toBeVisible()
  expect(breakdown.queryByText('After first token')).not.toBeInTheDocument()
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
    'Total input',
    'Output',
    'Read cache',
    'Write cache',
    'Hit Rate',
    'First token',
    'Speed',
  ]) {
    expect(screen.queryByText(label)).not.toBeInTheDocument()
  }
})
