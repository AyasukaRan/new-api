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
import { render, screen } from '@testing-library/react'
import { expect, test } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import { DetailsDialog } from '../dialogs/details-dialog'
import { TimingMetricsCell } from '../timing-metrics-cell'

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
})

test('uses the same millisecond first-token precision in log details', async () => {
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
            type: 2,
            content: '',
            model_name: 'deepseek-flash',
            use_time: 14,
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
})
