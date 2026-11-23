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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test } from 'vitest'

import { usageLogSchema } from '../../data/schema'
import { LogMetricsDetailDialog } from '../log-metrics-detail-dialog'

test('opens the selected receipt, switches tabs on the same request and resets on reopen', async () => {
  const user = userEvent.setup()
  const log = usageLogSchema.parse({
    id: 17,
    user_id: 1,
    created_at: 1795400000,
    type: 2,
    content: '',
    model_name: 'deepseek-flash',
    quota: 100,
    use_time: 2,
  })
  render(
    <LogMetricsDetailDialog
      log={log}
      other={null}
      isAdmin={false}
      initialTab='timing'
      trigger={<button type='button'>2 s</button>}
    />
  )
  const opener = screen.getByRole('button', { name: '2 s' })
  await user.click(opener)
  const dialog = await screen.findByRole('dialog', { name: 'Request details' })
  expect(within(dialog).getByText(/deepseek-flash/)).toBeVisible()
  const timing = within(dialog).getByRole('tab', { name: 'Timing breakdown' })
  expect(timing).toHaveAttribute('aria-selected', 'true')
  await user.click(within(dialog).getByRole('tab', { name: 'Cost breakdown' }))
  expect(
    within(dialog).getByRole('tabpanel', { name: 'Cost breakdown' })
  ).toBeVisible()
  expect(within(dialog).queryByText('Gross profit')).not.toBeInTheDocument()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(opener).toHaveFocus()
  await user.click(opener)
  expect(
    await screen.findByRole('tab', { name: 'Timing breakdown' })
  ).toHaveAttribute('aria-selected', 'true')
})
