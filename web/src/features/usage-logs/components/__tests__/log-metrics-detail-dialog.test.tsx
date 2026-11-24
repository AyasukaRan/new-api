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
import { LogMetricPopover } from '../log-metric-popover'
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
    request_id: 'local-receipt-17',
    prompt_tokens: 64,
    completion_tokens: 16,
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
  const tabList = within(dialog).getByRole('tablist', {
    name: 'Request details',
  })
  expect(tabList.parentElement).toHaveClass(
    'overflow-x-auto',
    'overflow-y-hidden'
  )
  const timing = within(dialog).getByRole('tab', { name: 'Timing breakdown' })
  expect(timing).toHaveAttribute('aria-selected', 'true')
  await user.click(within(dialog).getByRole('tab', { name: 'Overview' }))
  const overview = within(dialog).getByRole('tabpanel', { name: 'Overview' })
  expect(within(overview).getByText('local-receipt-17')).toBeVisible()
  expect(
    within(overview).getByRole('img', { name: 'Token distribution' })
  ).toBeVisible()
  expect(
    within(overview).getByRole('region', { name: 'Billing Details' })
  ).toBeVisible()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(
    within(tabList).getByRole('tab', { name: 'Cost breakdown' })
  ).toHaveFocus()
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

test('previews metrics on hover and keyboard focus, and dismisses without moving focus', async () => {
  const user = userEvent.setup()
  render(
    <LogMetricPopover
      label='Token Breakdown'
      trigger={<button type='button'>800</button>}
    >
      <span>Recorded total</span>
    </LogMetricPopover>
  )
  const amount = screen.getByRole('button', { name: '800' })
  expect(screen.queryByText('Recorded total')).not.toBeInTheDocument()
  await user.hover(amount)
  expect(
    await screen.findByRole('dialog', { name: 'Token Breakdown' })
  ).toBeVisible()
  expect(amount).not.toHaveFocus()
  await user.unhover(amount)
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.tab()
  expect(amount).toHaveFocus()
  expect(await screen.findByText('Recorded total')).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(amount).toHaveFocus()
})
