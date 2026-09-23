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
import { toast } from 'sonner'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

import type {
  Channel,
  ChannelBalanceMonitor,
  ChannelMonitoring,
} from '../../types'
import { ChannelBalanceSummary } from '../channel-balance-summary'
import { BalanceCell } from '../channels-columns'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { BalanceQueryDialog } from '../dialogs/balance-query-dialog'
import { ChannelMonitoringContent } from '../dialogs/channel-monitoring-dialog'

const clients: QueryClient[] = []
afterEach(() => clients.splice(0).forEach((client) => client.clear()))

const complete: ChannelBalanceMonitor = {
  checked_at: 1789300000,
  balance: 7,
  balance_updated_time: 1789300000,
  known_balance: 7,
  partial: false,
  success: true,
  configuration_changed: false,
  key_balances: [
    { index: 0, balance: 3 },
    { index: 1, balance: 4 },
  ],
}

function BalanceDialogFixture(props: { type?: number; disabled?: boolean }) {
  const { open, setOpen, setCurrentRow } = useChannels()
  return (
    <>
      {[1, 2].map((id) => (
        <Button
          key={id}
          onClick={() => {
            setCurrentRow({
              id,
              name: `Channel ${id}`,
              type: props.type ?? 1,
              setting: JSON.stringify({
                balance_query_disabled: props.disabled,
              }),
              balance: 7,
              balance_updated_time: complete.balance_updated_time,
              balance_monitor: complete,
            } as Channel)
            setOpen('balance-query')
          }}
        >
          Open channel {id}
        </Button>
      ))}
      <BalanceQueryDialog
        open={open === 'balance-query'}
        onOpenChange={(value) => !value && setOpen(null)}
      />
    </>
  )
}

function renderBalanceDialog(type = 1, disabled = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <BalanceDialogFixture type={type} disabled={disabled} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

function renderBalance(
  monitor?: ChannelBalanceMonitor,
  multi = true,
  disabled = false
) {
  const channel = {
    id: 1,
    type: 1,
    name: 'multi-key',
    setting: JSON.stringify({ balance_query_disabled: disabled }),
    balance: 7,
    balance_updated_time: 1789300000,
    used_quota: 0,
    balance_monitor: monitor,
    channel_info: {
      is_multi_key: multi,
      multi_key_size: 2,
      multi_key_polling_index: 0,
      multi_key_mode: 'random',
    },
  } as Channel
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <BalanceCell channel={channel} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

describe('channel balance monitoring', () => {
  test('disabled balance queries retain the total and key history without sending mouse or keyboard requests', async () => {
    const request = vi.spyOn(api, 'get')
    const user = userEvent.setup()
    renderBalance(complete, true, true)
    const badge = screen.getByRole('button', { name: 'Update balance' })
    expect(badge).toHaveAttribute('aria-disabled', 'true')
    expect(badge).toHaveTextContent('$7')
    expect(screen.getByText('Balance queries disabled')).toBeVisible()
    await user.hover(badge)
    const breakdown = await screen.findByTestId('multi-key-balances')
    expect(within(breakdown).getByText('$3')).toBeVisible()
    expect(within(breakdown).getByText('$4')).toBeVisible()
    expect(
      screen.queryByText('Click to update balance')
    ).not.toBeInTheDocument()
    await user.click(badge)
    badge.focus()
    await user.keyboard('{Enter} ')
    expect(request).not.toHaveBeenCalled()
  })

  test('opening a disabled balance dialog preserves the last total and disables updating', async () => {
    const request = vi.spyOn(api, 'get')
    const user = userEvent.setup()
    renderBalanceDialog(1, true)
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    expect(
      screen.getByRole('button', { name: 'Update Balance' })
    ).toBeDisabled()
    expect(
      screen.getByText(
        'Balance queries are disabled. Previously recorded balances are retained.'
      )
    ).toBeVisible()
    expect(screen.getByText('$7')).toBeVisible()
    expect(request).not.toHaveBeenCalled()
  })

  test('a stale enabled dialog translates the server rejection after balance queries are disabled', async () => {
    vi.spyOn(api, 'get').mockResolvedValueOnce({
      data: { success: false, message: 'channel balance query is disabled' },
    })
    const error = vi.spyOn(toast, 'error')
    const user = userEvent.setup()
    renderBalanceDialog()
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    await user.click(screen.getByRole('button', { name: 'Update Balance' }))
    await waitFor(() =>
      expect(error).toHaveBeenCalledWith(
        'Balance queries are disabled. Previously recorded balances are retained.'
      )
    )
    expect(screen.getByText('$7')).toBeVisible()
  })

  test('an old partial balance response cannot replace another channel after closing its dialog', async () => {
    const response = {
      data: {
        success: true,
        partial: true,
        balance_monitor: { ...complete, partial: true, known_balance: 3 },
      },
    }
    let finish: (value: typeof response) => void = () => undefined
    vi.spyOn(api, 'get').mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve
      })
    )
    const user = userEvent.setup()
    renderBalanceDialog()
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    await user.click(screen.getByRole('button', { name: 'Update Balance' }))
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await user.click(screen.getByRole('button', { name: 'Open channel 2' }))
    await act(async () => finish(response))
    expect(
      within(screen.getByRole('dialog')).getByText('Channel 2')
    ).toBeVisible()
    expect(
      within(screen.getByRole('dialog')).queryByText('Channel 1')
    ).not.toBeInTheDocument()
  })

  test('Codex usage remains available with balance queries disabled and ignores responses from a closed dialog', async () => {
    const oldResponse = {
      data: { success: true, data: { email: 'old@example.com' } },
    }
    let finish: (value: typeof oldResponse) => void = () => undefined
    vi.spyOn(api, 'get')
      .mockReturnValueOnce(
        new Promise((resolve) => {
          finish = resolve
        })
      )
      .mockResolvedValueOnce({
        data: { success: true, data: { email: 'current@example.com' } },
      })
    const user = userEvent.setup()
    renderBalanceDialog(57, true)
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    expect(await screen.findByText('current@example.com')).toBeVisible()
    await act(async () => finish(oldResponse))
    expect(screen.getByText('current@example.com')).toBeVisible()
    expect(screen.queryByText('old@example.com')).not.toBeInTheDocument()
  })

  test('shows a complete aggregate and exposes each key balance on hover', async () => {
    renderBalance(complete)
    const badge = screen.getByRole('button', { name: 'Update balance' })
    expect(badge).toHaveTextContent('$7')
    await userEvent.hover(badge)
    const breakdown = await screen.findByTestId('multi-key-balances')
    expect(within(breakdown).getByText('$3')).toBeVisible()
    expect(within(breakdown).getByText('$4')).toBeVisible()
  })

  test('a partial query retains the complete total and identifies the failed key', async () => {
    renderBalance({
      ...complete,
      partial: true,
      known_balance: 3,
      key_balances: [
        { index: 0, balance: 3 },
        { index: 1, balance: null, error: 'balance_query_failed' },
      ],
    })
    const badge = screen.getByRole('button', { name: 'Update balance' })
    expect(badge).toHaveTextContent('$7')
    await userEvent.hover(badge)
    expect(await screen.findByText('Partial result')).toBeVisible()
    expect(
      within(await screen.findByTestId('multi-key-balances')).getByText(
        'Query failed'
      )
    ).toBeVisible()
    expect(screen.getByText('Queried subtotal: $3')).toBeVisible()
  })

  test('changed accounts hide the old aggregate and its per-key amounts', async () => {
    renderBalance({
      ...complete,
      balance: null,
      configuration_changed: true,
      key_balances: [],
    })
    const badge = screen.getByRole('button', { name: 'Update balance' })
    expect(badge).toHaveTextContent('Not queried')
    await userEvent.hover(badge)
    expect(
      await screen.findByText(
        'Channel accounts changed. Refresh balances to see the current accounts.'
      )
    ).toBeVisible()
    expect(screen.queryByTestId('multi-key-balances')).not.toBeInTheDocument()
  })

  test('a single-key channel has no multi-key tooltip', async () => {
    renderBalance(complete, false)
    await userEvent.hover(
      screen.getByRole('button', { name: 'Update balance' })
    )
    expect(await screen.findByText('Click to update balance')).toBeVisible()
    expect(screen.queryByTestId('multi-key-balances')).not.toBeInTheDocument()
  })

  test('an initial failed query is unknown instead of zero and preserves failed account visibility', () => {
    render(
      <ChannelBalanceSummary
        monitor={{
          ...complete,
          balance: null,
          balance_updated_time: 0,
          known_balance: 0,
          success: false,
          key_balances: [{ index: 0, balance: null }],
        }}
      />
    )
    expect(screen.getByText('Not queried')).toBeVisible()
    expect(screen.getByText('Query failed')).toBeVisible()
    expect(
      screen.getByText(
        'The latest balance query failed. The last complete balance is retained.'
      )
    ).toBeVisible()
  })

  test('a channel without history displays actual usage independently of its unknown balance', () => {
    const data: ChannelMonitoring = {
      used_quota: 10000,
      balance: null,
      balance_history: [],
      usage: {
        channel_id: 1,
        hours: 24,
        request_count: 8,
        success_count: 6,
        success_rate: 75,
        input_tokens: 1200,
        output_tokens: 600,
        used_quota: 10000,
        recorded_used_quota: 10000,
        recorded_input_tokens: 1200,
        recorded_output_tokens: 600,
        billing_record_count: 6,
        probe_count: 100,
        avg_latency_ms: 230,
        series: [],
      },
    }
    render(<ChannelMonitoringContent data={data} />)
    expect(screen.getByText('8')).toBeVisible()
    expect(screen.getByText('75.0%')).toBeVisible()
    expect(screen.getByText('No balance history yet')).toBeInTheDocument()
    expect(screen.getByText('Not queried')).toBeVisible()
  })
})
