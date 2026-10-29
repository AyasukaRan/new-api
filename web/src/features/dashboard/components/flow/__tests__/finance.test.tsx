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
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { FlowCharts } from '../flow-charts'

// Canvas chart rendering is external to the controls and data projection under test.
vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: vi.fn() },
}))

const client = new QueryClient({
  defaultOptions: { queries: { retry: false } },
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

it('restricts cost controls and financial summary to administrators when the session role changes', async () => {
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null)
  const request = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: [
        {
          user_id: 2,
          username: 'fp',
          model_name: 'claude-opus-5-5',
          channel_id: 1,
          use_group: 'default',
          quota: 100,
          count: 1,
          token_used: 20,
          finance: {
            cost_quota: 130,
            profit_quota: -30,
            covered_revenue_quota: 100,
            recorded_count: 1,
            actual_count: 1,
            estimated_count: 0,
            unrecorded_count: 0,
          },
        },
      ],
    },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'admin', role: 10, quota: 0 })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <FlowCharts
        filters={{
          start_timestamp: new Date('2026-10-28T00:00:00Z'),
          end_timestamp: new Date('2026-10-29T00:00:00Z'),
          time_granularity: 'hour',
        }}
      />
    </QueryClientProvider>
  )
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith('/api/data/flow', expect.anything())
  )
  expect(
    screen.getByRole('region', { name: 'Financial summary' })
  ).toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Upstream cost' }))
  expect(screen.getByRole('tab', { name: 'Upstream cost' })).toHaveAttribute(
    'aria-selected',
    'true'
  )

  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'fp', role: 1, quota: 0 })
  )
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      '/api/data/flow/self',
      expect.anything()
    )
  )
  expect(
    screen.queryByRole('tab', { name: 'Upstream cost' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('region', { name: 'Financial summary' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'By quota' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
})
