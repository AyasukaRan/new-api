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
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it } from 'vitest'

import { MonitoredModelHotspots } from '../monitored-model-hotspots'

afterEach(cleanup)

it('ranks monitored traffic by volume or failures and leaves unmeasured first-token latency blank', async () => {
  const user = userEvent.setup()
  render(
    <MonitoredModelHotspots
      models={[
        {
          model_name: 'high volume',
          request_count: 100,
          success_count: 99,
          avg_latency_ms: 2100,
          avg_ttft_ms: 334,
          avg_tps: 4,
        },
        {
          model_name: 'most failures',
          request_count: 5,
          success_count: 1,
          avg_latency_ms: 270,
          avg_ttft_ms: null,
          avg_tps: 0,
        },
        {
          model_name: 'immediate',
          request_count: 1,
          success_count: 1,
          avg_latency_ms: 1,
          avg_ttft_ms: 0,
          avg_tps: 0,
        },
      ]}
    />
  )
  expect(screen.getByText('0ms')).toBeVisible()
  const rows = () => screen.getAllByRole('row').slice(1)
  expect(within(rows()[0]).getByRole('rowheader')).toHaveTextContent(
    'high volume'
  )
  expect(within(rows()[0]).getByText('334ms')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Failed' }))
  expect(within(rows()[0]).getByRole('rowheader')).toHaveTextContent(
    'most failures'
  )
  expect(within(rows()[0]).getByText('4')).toBeVisible()
  expect(within(rows()[0]).getByText('—')).toBeVisible()
  expect(screen.getByRole('columnheader', { name: 'Failed' })).toHaveAttribute(
    'aria-sort',
    'descending'
  )
  await user.click(screen.getByRole('button', { name: 'Failed' }))
  expect(within(rows()[0]).getByRole('rowheader')).toHaveTextContent(
    'immediate'
  )
})
