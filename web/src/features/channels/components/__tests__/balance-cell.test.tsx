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
import { ChannelRowActionsLayoutContext } from '../channel-row-actions-context'
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
  key_accounts: [
    { index: 0, groups: ['Low cost'], status: 1 },
    { index: 1, groups: ['Premium'], status: 2 },
  ],
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

function SelectedBalanceDialog() {
  const { open, setOpen } = useChannels()
  return (
    <BalanceQueryDialog
      open={open === 'balance-query'}
      onOpenChange={(value) => !value && setOpen(null)}
    />
  )
}

function HideBalancesButton() {
  const { setSensitiveVisible } = useChannels()
  return (
    <Button onClick={() => setSensitiveVisible(false)}>Hide balances</Button>
  )
}

function renderBalance(
  monitor?: ChannelBalanceMonitor,
  multi = true,
  disabled = false,
  layout: 'table' | 'card' = 'table'
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
        <HideBalancesButton />
        <ChannelRowActionsLayoutContext.Provider value={layout}>
          <BalanceCell channel={channel} />
        </ChannelRowActionsLayoutContext.Provider>
        <SelectedBalanceDialog />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

describe('channel balance monitoring', () => {
  test('disabled balance queries retain separate key balances without sending mouse or keyboard requests', async () => {
    const request = vi.spyOn(api, 'get')
    const user = userEvent.setup()
    renderBalance(complete, true, true)
    const badge = screen.getByRole('button', { name: 'Update balance' })
    expect(badge).toHaveAttribute('aria-disabled', 'true')
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
    expect(screen.getByText('Balance queries disabled')).toBeVisible()
    const breakdown = screen.getByTestId('multi-key-balances')
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

  test('opening a disabled balance dialog preserves separate balances and disables updating', async () => {
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
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
    expect(screen.getByText('$3')).toBeVisible()
    expect(screen.getByText('$4')).toBeVisible()
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
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
    expect(screen.getByText('$3')).toBeVisible()
    expect(screen.getByText('$4')).toBeVisible()
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

  test('shows each key balance and its own groups without a combined amount', () => {
    renderBalance(complete)
    const rows = screen.getByTestId('multi-key-balances')
    expect(within(rows).getByText('$3')).toBeVisible()
    expect(within(rows).getByText('$4')).toBeVisible()
    expect(within(rows).getByText('Low cost')).toBeVisible()
    expect(within(rows).getByText('Premium')).toBeVisible()
    expect(within(rows).getByText('Manual Disabled')).toBeVisible()
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
  })

  test('keeps configured key order, including unqueried accounts and equal balances', () => {
    renderBalance({
      ...complete,
      key_accounts: [
        { index: 1, groups: ['Premium'], status: 1 },
        { index: 2, groups: [], status: 1 },
        { index: 0, groups: ['Low cost'], status: 1 },
      ],
      key_balances: [
        { index: 0, balance: 3 },
        { index: 1, balance: 3 },
      ],
    })
    const rows = screen.getByTestId('multi-key-balances')
    expect(
      within(rows)
        .getAllByLabelText(/Key #/)
        .map((node) => node.textContent)
    ).toEqual(['#2', '#3', '#1'])
    expect(within(rows).getAllByText('$3')).toHaveLength(2)
    expect(within(rows).getByText('Not queried')).toBeVisible()
    expect(within(rows).getByText('No groups')).toBeVisible()
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
  })

  test('cards also separate key balances and hiding them removes amounts, groups and stale amounts', async () => {
    renderBalance(
      {
        ...complete,
        partial: true,
        key_balances: [
          { index: 0, balance: 3 },
          {
            index: 1,
            balance: null,
            last_known_balance: 4,
            error: 'balance_query_failed',
          },
        ],
      },
      true,
      false,
      'card'
    )
    expect(screen.getByText('$3')).toBeVisible()
    expect(screen.getByText('Last known balance: $4')).toBeVisible()
    await userEvent.click(screen.getByRole('button', { name: 'Hide balances' }))
    expect(screen.queryByText('$3')).not.toBeInTheDocument()
    expect(screen.queryByText(/Last known balance/)).not.toBeInTheDocument()
    expect(screen.queryByText('Low cost')).not.toBeInTheDocument()
    expect(screen.queryByText('Premium')).not.toBeInTheDocument()
    expect(document.querySelector('[title="$3"]')).toBeNull()
    expect(document.querySelector('[title="Premium"]')).toBeNull()
  })

  test('refreshes all key balances without announcing a combined amount', async () => {
    vi.spyOn(api, 'get').mockResolvedValueOnce({
      data: { success: true, balance: 7, balance_monitor: complete },
    })
    const success = vi.spyOn(toast, 'success')
    renderBalance(complete)
    await userEvent.click(
      screen.getByRole('button', { name: 'Update balance' })
    )
    await waitFor(() =>
      expect(success).toHaveBeenCalledWith('Balance updated successfully')
    )
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
  })

  test('keeps the list short and opens every current key in the balance dialog', async () => {
    const monitor = {
      ...complete,
      key_accounts: [0, 1, 2, 3].map((index) => ({
        index,
        status: 1,
        groups: [`Group ${index + 1}`],
      })),
      key_balances: [0, 1, 2, 3].map((index) => ({
        index,
        balance: 10 + index,
      })),
    }
    renderBalance(monitor)
    expect(screen.getByText('$10')).toBeVisible()
    expect(screen.queryByText('$13')).not.toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'View all 4 keys' })
    )
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('Key #4')).toBeVisible()
    expect(within(dialog).getByText('Group 4')).toBeVisible()
    expect(within(dialog).getByText('$13')).toBeVisible()
    expect(within(dialog).queryByText('$7')).not.toBeInTheDocument()
  })

  test('multi-key accounts without any query do not fall back to the old aggregate', () => {
    renderBalance()
    expect(screen.getByText('Not queried')).toBeVisible()
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
  })

  test.each(['list', 'dialog'] as const)(
    'preserves an unrecognized custom balance response from the %s even when a monitor is returned',
    async (entry) => {
      vi.spyOn(api, 'get').mockResolvedValueOnce({
        data: {
          success: true,
          raw_response: '{"credits":"unrecognized format"}',
          balance_monitor: { ...complete, success: false },
        },
      })
      const success = vi.spyOn(toast, 'success')
      if (entry === 'list') {
        renderBalance(complete)
        await userEvent.click(
          screen.getByRole('button', { name: 'Update balance' })
        )
      } else {
        renderBalanceDialog()
        await userEvent.click(
          screen.getByRole('button', { name: 'Open channel 1' })
        )
        await userEvent.click(
          screen.getByRole('button', { name: 'Update Balance' })
        )
      }
      expect(
        await screen.findByText('Balance response not recognized')
      ).toBeVisible()
      expect(
        await screen.findByLabelText('Upstream JSON response')
      ).toHaveTextContent('unrecognized format')
      expect(success).not.toHaveBeenCalled()
    }
  )

  test('partial queries show the failed key and its stale value without a subtotal', async () => {
    renderBalance({
      ...complete,
      partial: true,
      known_balance: 3,
      key_balances: [
        { index: 0, balance: 3 },
        {
          index: 1,
          balance: null,
          last_known_balance: 4,
          error: 'balance_query_failed',
        },
      ],
    })
    const rows = screen.getByTestId('multi-key-balances')
    expect(within(rows).getByText('$3')).toBeVisible()
    expect(within(rows).getByText('Query failed')).toBeVisible()
    expect(within(rows).getByText('Last known balance: $4')).toBeVisible()
    await userEvent.hover(
      screen.getByRole('button', { name: 'Update balance' })
    )
    expect(await screen.findByText('Partial result')).toBeVisible()
    expect(screen.queryByText(/Queried subtotal/)).not.toBeInTheDocument()
    expect(screen.queryByText('$7')).not.toBeInTheDocument()
  })

  test('changed accounts retain current groups but never reuse old key amounts', async () => {
    renderBalance({ ...complete, balance: null, configuration_changed: true })
    const rows = screen.getByTestId('multi-key-balances')
    expect(within(rows).getAllByText('Not queried')).toHaveLength(2)
    expect(within(rows).getByText('Low cost')).toBeVisible()
    expect(screen.queryByText('$3')).not.toBeInTheDocument()
    expect(screen.queryByText('$4')).not.toBeInTheDocument()
    await userEvent.hover(
      screen.getByRole('button', { name: 'Update balance' })
    )
    expect(
      await screen.findByText(
        'Channel accounts changed. Refresh balances to see the current accounts.'
      )
    ).toBeVisible()
  })

  test('a single-key channel has no multi-key tooltip', async () => {
    renderBalance(
      {
        ...complete,
        key_accounts: [{ index: 0, groups: ['default'], status: 1 }],
        key_balances: [{ index: 0, balance: 7 }],
      },
      false
    )
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
          key_accounts: [{ index: 0, groups: ['default'], status: 1 }],
          key_balances: [
            { index: 0, balance: null, error: 'balance_query_failed' },
          ],
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

  test.each([false, true])(
    'monitoring preserves usage and shows only per-key historical balances when present: %s',
    (withHistory) => {
      const data: ChannelMonitoring = {
        used_quota: 10000,
        balance: withHistory ? complete : null,
        balance_history: withHistory
          ? [
              {
                ...complete,
                id: 1,
                channel_id: 1,
                used_quota: 10000,
                key_balances: [
                  { index: 0, balance: 3 },
                  {
                    index: 1,
                    balance: null,
                    last_known_balance: 4,
                    error: 'balance_query_failed',
                  },
                ],
              },
            ]
          : [],
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
      if (withHistory) {
        const history = screen.getByRole('region', { name: 'Balance history' })
        expect(within(history).getByText('Key #1')).toBeVisible()
        expect(within(history).getByText('$3')).toBeVisible()
        expect(
          within(history).getByText('Last known balance: $4')
        ).toBeVisible()
        expect(within(history).queryByText('Premium')).not.toBeInTheDocument()
        expect(history.querySelector('svg')).toBeNull()
        expect(screen.queryByText('$7')).not.toBeInTheDocument()
        expect(
          screen.getByText(
            'Key numbers reflect the configuration at query time.'
          )
        ).toBeVisible()
      } else {
        expect(screen.getByText('No balance history yet')).toBeInTheDocument()
        expect(screen.getByText('Not queried')).toBeVisible()
      }
    }
  )
})
