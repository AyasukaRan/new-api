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
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, test, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'

import type { Channel, MultiKeyManageParams } from '../../types'
import { useChannelsColumns } from '../channels-columns'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { MultiKeyManageDialog } from '../dialogs/multi-key-manage-dialog'

const clients: QueryClient[] = []
afterEach(() => clients.splice(0).forEach((client) => client.clear()))

function statusResponse(overrides: Record<string, unknown> = {}) {
  return {
    data: {
      success: true,
      data: {
        keys: [
          { index: 0, status: 1, key_preview: 'sk-a**********0001' },
          {
            index: 1,
            status: 2,
            key_preview: 'sk-b**********0002',
            reason: 'manual',
            disabled_time: 1789300000,
          },
          { index: 2, status: 1, key_preview: 'sk-c**********0003' },
        ],
        total: 3,
        page: 1,
        page_size: 10,
        total_pages: 1,
        enabled_count: 2,
        manual_disabled_count: 1,
        auto_disabled_count: 0,
        balance_query_disabled: false,
        balance_monitor: {
          checked_at: 1789300000,
          balance: 3,
          balance_updated_time: 1789200000,
          known_balance: 0,
          partial: true,
          success: false,
          configuration_changed: false,
          key_balances: [
            { index: 0, balance: 0 },
            { index: 1, balance: -2 },
            {
              index: 2,
              balance: null,
              error: 'balance_query_failed',
              last_known_balance: 5,
            },
          ],
        },
        ...overrides,
      },
    },
  }
}

const entryChannels = [
  {
    id: 3,
    name: 'Keyboard channel',
    type: 1,
    channel_info: {
      is_multi_key: true,
      multi_key_size: 3,
      multi_key_mode: 'polling',
    },
  } as Channel,
]

function KeyManagementEntry() {
  const table = useReactTable({
    data: entryChannels,
    columns: useChannelsColumns({ enableSelection: false }),
    getCoreRowModel: getCoreRowModel(),
  })
  const cell = table
    .getRowModel()
    .rows[0]?.getAllCells()
    .find((item) => item.column.id === 'type')
  return cell ? flexRender(cell.column.columnDef.cell, cell.getContext()) : null
}

function Fixture() {
  const { open, setOpen, setCurrentRow } = useChannels()
  return (
    <>
      <KeyManagementEntry />
      {[1, 2].map((id) => (
        <Button
          key={id}
          onClick={() => {
            setCurrentRow({ id, name: `Channel ${id}`, type: 1 } as Channel)
            setOpen('multi-key-manage')
          }}
        >
          Open channel {id}
        </Button>
      ))}
      <MultiKeyManageDialog
        open={open === 'multi-key-manage'}
        onOpenChange={(value) => !value && setOpen(null)}
      />
    </>
  )
}

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <Fixture />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  return { client, user: userEvent.setup() }
}

function keyRow(index: number) {
  const row = screen.getByText(`#${index}`).closest('tr')
  if (!row) throw new Error(`Missing key row ${index}`)
  return within(row)
}

test('shows masked keys and distinguishes zero, negative and last known balances', async () => {
  vi.spyOn(api, 'post').mockResolvedValue(statusResponse())
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(await screen.findByText('sk-a**********0001')).toBeVisible()
  expect(keyRow(1).getByText('$0')).toBeVisible()
  expect(keyRow(2).getByText('-$2')).toBeVisible()
  expect(keyRow(3).getByText('Query failed')).toBeVisible()
  expect(keyRow(3).getByText('Last known balance: $5')).toBeVisible()
  expect(keyRow(1).getByText(/Last queried:/)).toBeVisible()
})

test('disabling balance queries retains cached readings and never sends an upstream balance query', async () => {
  vi.spyOn(api, 'post').mockResolvedValue(
    statusResponse({ balance_query_disabled: true })
  )
  const get = vi.spyOn(api, 'get')
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(await screen.findByText('sk-a**********0001')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Update Balance' })).toBeDisabled()
  expect(
    screen.getByText(
      'Balance queries are disabled. Previously recorded balances are retained.'
    )
  ).toBeVisible()
  expect(keyRow(1).getByText('$0')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Update Balance' }))
  expect(get).not.toHaveBeenCalled()
})

test('a changed key configuration cannot display previously indexed balances', async () => {
  const response = statusResponse()
  response.data.data.balance_monitor.configuration_changed = true
  vi.spyOn(api, 'post').mockResolvedValue(response)
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(await screen.findByText('sk-a**********0001')).toBeVisible()
  expect(keyRow(1).getByText('Not queried')).toBeVisible()
  expect(screen.queryByText('$0')).not.toBeInTheDocument()
  expect(screen.queryByText('Last known balance: $5')).not.toBeInTheDocument()
})

test('disables the selected key and refreshes both channel and monitoring data', async () => {
  let disabled = false
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, params) => {
      if ((params as MultiKeyManageParams).action === 'disable_key') {
        disabled = true
        return { data: { success: true } }
      }
      const response = statusResponse()
      if (disabled) response.data.data.keys[0].status = 2
      return response
    })
  const { client, user } = setup()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.click(keyRow(1).getByRole('button', { name: 'Disable' }))
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Continue',
    })
  )
  await waitFor(() =>
    expect(keyRow(1).getByRole('button', { name: 'Enable' })).toBeEnabled()
  )
  expect(post).toHaveBeenCalledWith(
    '/api/channel/multi_key/manage',
    { channel_id: 1, action: 'disable_key', key_index: 0 },
    expect.anything()
  )
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['channels'] })
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['channel-monitoring'] })
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['perf-metrics-admin'] })
})

test('updating balances reloads the current page and channel list', async () => {
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, params) =>
    statusResponse({
      page: (params as MultiKeyManageParams).page,
      total_pages: 2,
    })
  )
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({ data: { success: true } })
  const { client, user } = setup()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await screen.findByText('Page 2 of 2')
  post.mockClear()
  await user.click(screen.getByRole('button', { name: 'Update Balance' }))
  await waitFor(() => expect(post).toHaveBeenCalled())
  expect(get).toHaveBeenCalledWith(
    '/api/channel/update_balance/1',
    expect.anything()
  )
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({ channel_id: 1, page: 2 })
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['channels'] })
})

test('closing and reopening a channel ignores the old status response', async () => {
  let finish!: (value: ReturnType<typeof statusResponse>) => void
  vi.spyOn(api, 'post')
    .mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve
      })
    )
    .mockResolvedValue(
      statusResponse({
        keys: [{ index: 0, status: 1, key_preview: 'new-**********key' }],
      })
    )
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(await screen.findByText('new-**********key')).toBeVisible()
  await act(async () => finish(statusResponse()))
  expect(screen.getByText('new-**********key')).toBeVisible()
  expect(screen.queryByText('sk-a**********0001')).not.toBeInTheDocument()
})

test('a completed balance update from a closed channel cannot replace a new channel', async () => {
  vi.spyOn(api, 'post').mockImplementation(async (_url, params) =>
    statusResponse({
      keys: [
        {
          index: 0,
          status: 1,
          key_preview: `key-${(params as MultiKeyManageParams).channel_id}**********hint`,
        },
      ],
    })
  )
  let finish!: (value: { data: { success: boolean } }) => void
  vi.spyOn(api, 'get').mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    })
  )
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('key-1**********hint')
  await user.click(screen.getByRole('button', { name: 'Update Balance' }))
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.click(screen.getByRole('button', { name: 'Open channel 2' }))
  expect(await screen.findByText('key-2**********hint')).toBeVisible()
  await act(async () => finish({ data: { success: true } }))
  expect(screen.getByText('key-2**********hint')).toBeVisible()
  expect(screen.queryByText('key-1**********hint')).not.toBeInTheDocument()
})

test('the shared table and card multi-key entry opens management with a keyboard', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue(statusResponse())
  const { user } = setup()
  screen.getByRole('button', { name: 'Manage Keys' }).focus()
  await user.keyboard('{Enter}')
  expect(await screen.findByText('sk-a**********0001')).toBeVisible()
  expect(
    within(screen.getByRole('dialog')).getByText('Keyboard channel')
  ).toBeVisible()
  expect(post.mock.calls[0]?.[1]).toMatchObject({
    channel_id: 3,
    action: 'get_key_status',
  })
})

test('a failed status request exposes retry without enabling any key action', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({ data: { success: false, message: 'unavailable' } })
    .mockResolvedValue(statusResponse({ balance_monitor: null }))
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(await screen.findByText('Failed to load key status')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Update Balance' })).toBeDisabled()
  expect(
    screen.queryByRole('button', { name: 'Disable' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('sk-a**********0001')).toBeVisible()
  expect(keyRow(1).getByText('Not queried')).toBeVisible()
  expect(post).toHaveBeenCalledTimes(2)
})

test('an all-failed balance query replaces old success with a failed reading and last-known balance', async () => {
  let refreshed = false
  vi.spyOn(api, 'post').mockImplementation(async () => {
    const response = statusResponse()
    response.data.data.balance_monitor.key_balances[0] = refreshed
      ? {
          index: 0,
          balance: null,
          error: 'balance_query_failed',
          last_known_balance: 7,
        }
      : { index: 0, balance: 7 }
    return response
  })
  vi.spyOn(api, 'get').mockImplementation(async () => {
    refreshed = true
    return { data: { success: false, message: 'balance_query_failed' } }
  })
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  expect(keyRow(1).getByText('$7')).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Update Balance' }))
  await waitFor(() => expect(keyRow(1).getByText('Query failed')).toBeVisible())
  expect(keyRow(1).getByText('Last known balance: $7')).toBeVisible()
  expect(keyRow(1).queryByText('$7')).not.toBeInTheDocument()
})

test('a refreshed key snapshot dismisses an unsubmitted confirmation', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce(statusResponse())
    .mockResolvedValue(
      statusResponse({
        keys: [
          { index: 0, status: 1, key_preview: 'replacement-**********hint' },
        ],
      })
    )
  const { client, user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.click(keyRow(1).getByRole('button', { name: 'Disable' }))
  expect(screen.getByRole('alertdialog')).toBeVisible()
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['channels'] })
  })
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(screen.getByText('replacement-**********hint')).toBeVisible()
  expect(
    post.mock.calls.every(
      (call) => (call[1] as MultiKeyManageParams).action === 'get_key_status'
    )
  ).toBe(true)
})
