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
import i18next, { createInstance, type i18n } from 'i18next'
import { useEffect, useState } from 'react'
import { I18nextProvider } from 'react-i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { channelsQueryKeys } from '../../lib'
import {
  channelSchema,
  type Channel,
  type KeyStatus,
  type MultiKeyManageParams,
} from '../../types'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { MultiKeyManageDialog } from '../dialogs/multi-key-manage-dialog'

const clients: QueryClient[] = []
const originalAuth = useAuthStore.getState().auth
beforeEach(() => {
  originalAuth.setUser({
    id: 1,
    username: 'operator',
    role: ROLE.ADMIN,
    permissions: { admin_permissions: { channel: { operate: true } } },
  })
})
afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.setState({ auth: originalAuth })
})

function statusResponse(overrides: Record<string, unknown> = {}) {
  return {
    data: {
      success: true,
      data: {
        keys: [
          {
            index: 0,
            status: 1,
            key_preview: 'sk-a**********0001',
            key_identity: 'identity-a',
            groups: [] as string[],
            upstream_cost_ratio: null as number | null,
          },
          {
            index: 1,
            key_identity: 'identity-b',
            groups: ['default'],
            upstream_cost_ratio: null as number | null,
            status: 2,
            key_preview: 'sk-b**********0002',
            reason: 'manual',
            disabled_time: 1789300000,
          },
          {
            index: 2,
            status: 1,
            key_preview: 'sk-c**********0003',
            key_identity: 'identity-c',
            groups: [] as string[],
            upstream_cost_ratio: null as number | null,
          },
        ],
        channel_groups: ['default', 'premium'],
        channel_upstream_cost_ratio: 1.25,
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

function Fixture(props: { balanceRechargeMultiplier?: number }) {
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
              type: 1,
              setting: JSON.stringify({
                balance_recharge_multiplier: props.balanceRechargeMultiplier,
              }),
            } as Channel)
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

function setup(language?: i18n, balanceRechargeMultiplier?: number) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <I18nextProvider i18n={language ?? i18next}>
      <QueryClientProvider client={client}>
        <ChannelsProvider>
          <Fixture balanceRechargeMultiplier={balanceRechargeMultiplier} />
        </ChannelsProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
  return { client, user: userEvent.setup() }
}

function keyRow(index: number) {
  const row = screen.getByText(`#${index}`).closest('tr')
  if (!row) throw new Error(`Missing key row ${index}`)
  return within(row)
}

test.each([1, 2])(
  'key management converts balances with multiplier %s while retaining zero and failed states',
  async (multiplier) => {
    vi.spyOn(api, 'post').mockResolvedValue(statusResponse())
    const { user } = setup(undefined, multiplier)
    await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
    expect(await screen.findByText('sk-a**********0001')).toBeVisible()
    expect(keyRow(1).getByText('$0')).toBeVisible()
    expect(keyRow(2).getByText(`-$${2 / multiplier}`)).toBeVisible()
    expect(keyRow(3).getByText('Query failed')).toBeVisible()
    expect(
      keyRow(3).getByText(`Last known balance: $${5 / multiplier}`)
    ).toBeVisible()
    expect(keyRow(1).getByText(/Last queried:/)).toBeVisible()
  }
)

test('a fixed invalid credential reason follows the interface language', async () => {
  const response = statusResponse()
  response.data.data.keys[1].reason = 'Upstream API credential is invalid.'
  vi.spyOn(api, 'post').mockResolvedValue(response)
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { zh },
    keySeparator: false,
    interpolation: { escapeValue: false },
  })
  const { user } = setup(language)
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  expect(
    await screen.findByText('Upstream API credential is invalid.')
  ).toBeVisible()
  await act(() => language.changeLanguage('zh'))
  expect(screen.getByText('上游 API 凭据无效。')).toBeVisible()
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

test('key groups save the masked key identity and empty selection inherits channel groups', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  let groups: string[] = []
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, params) => {
      const request = params as MultiKeyManageParams
      if (request.action === 'set_key_groups') {
        groups = request.groups ?? []
        return { data: { success: true } }
      }
      const response = statusResponse()
      response.data.data.keys[0].groups = groups
      return response
    })
  const get = vi.spyOn(api, 'get')
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  expect(keyRow(1).getByText('All channel groups')).toBeVisible()
  await user.click(
    keyRow(1).getByRole('combobox', { name: 'Groups for key #1' })
  )
  await user.click(screen.getByRole('option', { name: 'premium' }))
  await user.keyboard('{Escape}')
  await user.click(keyRow(1).getByRole('button', { name: 'Save groups' }))
  await waitFor(() =>
    expect(
      keyRow(1).getByRole('button', { name: 'Save groups' })
    ).toBeDisabled()
  )
  expect(post).toHaveBeenCalledWith(
    '/api/channel/multi_key/manage',
    {
      action: 'set_key_groups',
      channel_id: 1,
      key_index: 0,
      key_identity: 'identity-a',
      groups: ['premium'],
    },
    expect.anything()
  )
  await user.click(
    keyRow(1).getByRole('combobox', { name: 'Groups for key #1' })
  )
  await user.click(screen.getByRole('option', { name: 'premium' }))
  await user.keyboard('{Escape}')
  await user.click(keyRow(1).getByRole('button', { name: 'Save groups' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel/multi_key/manage',
      {
        action: 'set_key_groups',
        channel_id: 1,
        key_index: 0,
        key_identity: 'identity-a',
        groups: [],
      },
      expect.anything()
    )
  )
  expect(get).not.toHaveBeenCalled()
  expect(
    post.mock.calls.every(([url]) => url === '/api/channel/multi_key/manage')
  ).toBe(true)
})

test('failed key group saving preserves the draft but a replacement identity cannot inherit it', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  let replaced = false
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, params) => {
      if ((params as MultiKeyManageParams).action === 'set_key_groups') {
        return {
          data: {
            success: false,
            message: 'The key list changed. Refresh and try again.',
          },
        }
      }
      const response = statusResponse()
      if (replaced) {
        response.data.data.keys[0].key_identity = 'replacement-identity'
        response.data.data.keys[0].key_preview = 'replacement-**********hint'
      }
      return response
    })
  const { client, user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.click(
    keyRow(1).getByRole('combobox', { name: 'Groups for key #1' })
  )
  await user.click(screen.getByRole('option', { name: 'premium' }))
  await user.keyboard('{Escape}')
  await user.click(keyRow(1).getByRole('button', { name: 'Save groups' }))
  await waitFor(() =>
    expect(keyRow(1).getByRole('button', { name: 'Save groups' })).toBeEnabled()
  )
  expect(keyRow(1).getByText('premium')).toBeVisible()
  expect(
    post.mock.calls.filter(
      ([, params]) =>
        (params as MultiKeyManageParams).action === 'get_key_status'
    ).length
  ).toBeGreaterThan(1)
  replaced = true
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['channels'] })
  })
  expect(await screen.findByText('replacement-**********hint')).toBeVisible()
  expect(keyRow(1).getByText('All channel groups')).toBeVisible()
  expect(keyRow(1).getByRole('button', { name: 'Save groups' })).toBeDisabled()
  expect(keyRow(1).queryByText('premium')).not.toBeInTheDocument()
})

test('key settings require sensitive write permission and fit the desktop dialog with narrow-screen scrolling', async () => {
  vi.spyOn(api, 'post').mockResolvedValue(statusResponse())
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  expect(
    keyRow(1).getByRole('combobox', { name: 'Groups for key #1' })
  ).toBeDisabled()
  expect(keyRow(1).getByRole('button', { name: 'Save groups' })).toBeDisabled()
  expect(
    keyRow(1).getByRole('textbox', {
      name: 'Upstream cost multiplier for key #1',
    })
  ).toBeDisabled()
  expect(keyRow(1).getByRole('button', { name: 'Save cost' })).toBeDisabled()
  expect(screen.getByRole('dialog')).toHaveClass(
    'sm:max-w-6xl',
    'max-w-[calc(100%-2rem)]'
  )
  expect(screen.getByRole('table')).toHaveClass('min-w-[64rem]')
  expect(screen.getByRole('table').parentElement).toHaveClass('overflow-x-auto')
})

test('key cost saves explicit zero and clearing it restores channel inheritance with an explicit flag', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  let ratio: number | null = null
  const post = vi
    .spyOn(api, 'post')
    .mockImplementation(async (_url, params) => {
      const request = params as {
        action: string
        upstream_cost_ratio?: number
        clear_cost_ratio?: boolean
      }
      if (request.action === 'set_key_cost_ratio') {
        ratio = request.clear_cost_ratio
          ? null
          : (request.upstream_cost_ratio ?? null)
        return { data: { success: true } }
      }
      const response = statusResponse()
      response.data.data.keys[0].upstream_cost_ratio = ratio
      return response
    })
  const get = vi.spyOn(api, 'get')
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  const input = keyRow(1).getByRole('textbox', {
    name: 'Upstream cost multiplier for key #1',
  })
  expect(input).toHaveValue('')
  expect(keyRow(1).getByText('Channel cost: 1.25×')).toBeVisible()
  await user.type(input, '0')
  await user.click(keyRow(1).getByRole('button', { name: 'Save cost' }))
  await waitFor(() =>
    expect(keyRow(1).getByRole('button', { name: 'Save cost' })).toBeDisabled()
  )
  expect(input).toHaveValue('0')
  expect(keyRow(1).getByText('Free upstream cost')).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/multi_key/manage',
    {
      action: 'set_key_cost_ratio',
      channel_id: 1,
      key_index: 0,
      key_identity: 'identity-a',
      upstream_cost_ratio: 0,
    },
    expect.anything()
  )
  await user.clear(input)
  await user.click(keyRow(1).getByRole('button', { name: 'Save cost' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/channel/multi_key/manage',
      {
        action: 'set_key_cost_ratio',
        channel_id: 1,
        key_index: 0,
        key_identity: 'identity-a',
        clear_cost_ratio: true,
      },
      expect.anything()
    )
  )
  expect(get).not.toHaveBeenCalled()
})

test('invalid key cost drafts cannot be saved', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue(statusResponse())
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  const input = keyRow(1).getByRole('textbox', {
    name: 'Upstream cost multiplier for key #1',
  })
  for (const value of ['-1', '10001', 'invalid']) {
    await user.clear(input)
    await user.type(input, value)
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(keyRow(1).getByRole('button', { name: 'Save cost' })).toBeDisabled()
  }
  expect(
    post.mock.calls.every(
      ([, params]) => (params as { action: string }).action === 'get_key_status'
    )
  ).toBe(true)
})

test('failed key cost saving retains the draft but a replacement key cannot inherit it', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  let replaced = false
  vi.spyOn(api, 'post').mockImplementation(async (_url, params) => {
    if ((params as { action: string }).action === 'set_key_cost_ratio') {
      return { data: { success: false, message: 'Operation failed' } }
    }
    const response = statusResponse()
    if (replaced) {
      response.data.data.keys[0].key_identity = 'replacement-identity'
    }
    return response
  })
  const { client, user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.type(
    keyRow(1).getByRole('textbox', {
      name: 'Upstream cost multiplier for key #1',
    }),
    '0.5'
  )
  await user.click(keyRow(1).getByRole('button', { name: 'Save cost' }))
  await waitFor(() =>
    expect(keyRow(1).getByRole('button', { name: 'Save cost' })).toBeEnabled()
  )
  expect(
    keyRow(1).getByRole('textbox', {
      name: 'Upstream cost multiplier for key #1',
    })
  ).toHaveValue('0.5')
  replaced = true
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['channels'] })
  })
  await waitFor(() =>
    expect(
      keyRow(1).getByRole('textbox', {
        name: 'Upstream cost multiplier for key #1',
      })
    ).toHaveValue('')
  )
  expect(keyRow(1).getByRole('button', { name: 'Save cost' })).toBeDisabled()
})

test('a failed group save retains its draft through a failed status refresh', async () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  let statusUnavailable = false
  vi.spyOn(api, 'post').mockImplementation(async (_url, params) => {
    if ((params as MultiKeyManageParams).action === 'set_key_groups') {
      statusUnavailable = true
      return { data: { success: false, message: 'Operation failed' } }
    }
    return statusUnavailable
      ? { data: { success: false, message: 'Operation failed' } }
      : statusResponse()
  })
  const { user } = setup()
  await user.click(screen.getByRole('button', { name: 'Open channel 1' }))
  await screen.findByText('sk-a**********0001')
  await user.click(
    keyRow(1).getByRole('combobox', { name: 'Groups for key #1' })
  )
  await user.click(screen.getByRole('option', { name: 'premium' }))
  await user.keyboard('{Escape}')
  await user.click(keyRow(1).getByRole('button', { name: 'Save groups' }))
  expect(await screen.findByText('Failed to load key status')).toBeVisible()
  statusUnavailable = false
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await screen.findByText('sk-a**********0001')
  expect(keyRow(1).getByText('premium')).toBeVisible()
  expect(keyRow(1).getByRole('button', { name: 'Save groups' })).toBeEnabled()
})

const multiKeyChannel = {
  id: 7,
  type: 61,
  key: '',
  name: 'Account Live',
  status: 1,
  created_time: 1,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
  channel_info: {
    is_multi_key: true,
    multi_key_size: 2,
    multi_key_polling_index: 0,
    multi_key_mode: 'random',
  },
}

function keyStatusResponse(keys: Pick<KeyStatus, 'index' | 'status'>[]) {
  const enabled = keys.filter((key) => key.status === 1).length
  return {
    data: {
      success: true,
      data: {
        keys: keys.map((key) => ({
          ...key,
          key_identity: `identity-${key.index}`,
          groups: [],
          upstream_cost_ratio: null,
        })),
        total: keys.length,
        page: 1,
        page_size: 10,
        total_pages: 1,
        enabled_count: enabled,
        manual_disabled_count: 0,
        auto_disabled_count: keys.length - enabled,
      },
    },
  }
}

function OpenDialog(props: { channel: Channel; masked?: boolean }) {
  const { setCurrentRow, setSensitiveVisible } = useChannels()
  useEffect(() => {
    setCurrentRow(props.channel)
    setSensitiveVisible(!props.masked)
  }, [props.channel, props.masked, setCurrentRow, setSensitiveVisible])
  return <MultiKeyManageDialog open onOpenChange={() => {}} />
}

test('translates the disabled reasons the server writes and shows upstream text as written', async () => {
  const language = createInstance()
  await language.init({
    lng: 'zhCN',
    resources: {
      zhCN: {
        translation: {
          'Plugin credential needs a new login': '插件凭证需要重新登录',
        },
      },
    },
    keySeparator: false,
  })
  const upstream = 'upstream said {{bad}}'
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [
          {
            index: 0,
            status: 3,
            disabled_time: 1,
            reason: 'Plugin credential needs a new login',
          },
          { index: 1, status: 3, disabled_time: 1, reason: upstream },
        ],
        total: 2,
        page: 1,
        page_size: 10,
        total_pages: 1,
        enabled_count: 0,
        manual_disabled_count: 0,
        auto_disabled_count: 2,
      },
    },
  })
  const queryClient = new QueryClient()
  clients.push(queryClient)
  const channel = channelSchema.parse(multiKeyChannel)

  render(
    <I18nextProvider i18n={language}>
      <QueryClientProvider client={queryClient}>
        <ChannelsProvider>
          <OpenDialog channel={channel} />
        </ChannelsProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )

  expect(await screen.findByText('插件凭证需要重新登录')).toBeInTheDocument()
  expect(screen.getByText(upstream)).toBeInTheDocument()
})

/** Opens Manage Keys from inside another dialog, as the channel drawer does. */
function ManageKeysInsideDialog(props: { channel: Channel }) {
  const { setCurrentRow } = useChannels()
  const [editOpen, setEditOpen] = useState(true)
  const [manageOpen, setManageOpen] = useState(false)
  useEffect(() => {
    setCurrentRow(props.channel)
  }, [props.channel, setCurrentRow])
  return (
    <Dialog open={editOpen} onOpenChange={setEditOpen} title='Edit Channel'>
      <Button onClick={() => setManageOpen(true)}>Manage Keys</Button>
      <MultiKeyManageDialog open={manageOpen} onOpenChange={setManageOpen} />
    </Dialog>
  )
}

test('after a key is deleted, Escape closes Manage Keys and not the dialog that opened it', async () => {
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'root', role: ROLE.SUPER_ADMIN })
  const post = vi.spyOn(api, 'post')
  post
    .mockResolvedValueOnce(
      keyStatusResponse([
        { index: 0, status: 1 },
        { index: 1, status: 1 },
      ])
    )
    .mockResolvedValueOnce({ data: { success: true, message: '' } })
    .mockResolvedValueOnce(keyStatusResponse([{ index: 0, status: 1 }]))
  const queryClient = new QueryClient()
  clients.push(queryClient)
  const credentialsKey = channelsQueryKeys.pluginCredentials(7)
  queryClient.setQueryData(credentialsKey, [])
  const user = userEvent.setup()

  render(
    <QueryClientProvider client={queryClient}>
      <ChannelsProvider>
        <ManageKeysInsideDialog
          channel={channelSchema.parse(multiKeyChannel)}
        />
      </ChannelsProvider>
    </QueryClientProvider>
  )

  await user.click(screen.getByRole('button', { name: 'Manage Keys' }))
  const manage = await screen.findByRole('dialog', {
    name: /Multi-Key Management/,
  })
  const row = await within(manage).findByRole('row', { name: /^#2\s/ })
  await user.click(within(row).getByRole('button', { name: 'Delete' }))
  await user.click(await screen.findByRole('button', { name: 'Continue' }))

  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(within(manage).queryByText('#2')).not.toBeInTheDocument()
  expect(post).toHaveBeenNthCalledWith(
    2,
    '/api/channel/multi_key/manage',
    { channel_id: 7, action: 'delete_key', key_index: 1 },
    expect.anything()
  )
  // The credential panel in the edit drawer shows the lines that are left.
  expect(queryClient.getQueryState(credentialsKey)?.isInvalidated).toBe(true)
  await waitFor(() =>
    expect(manage.contains(document.activeElement)).toBe(true)
  )

  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(
      screen.queryByRole('dialog', { name: /Multi-Key Management/ })
    ).not.toBeInTheDocument()
  )
  expect(screen.getByRole('dialog', { name: 'Edit Channel' })).toBeVisible()
})

test('a plugin channel shows each key its own balance', async () => {
  vi.spyOn(api, 'post').mockResolvedValue(
    keyStatusResponse([
      { index: 0, status: 1 },
      { index: 1, status: 1 },
      { index: 2, status: 1 },
    ])
  )
  const queryClient = new QueryClient()
  clients.push(queryClient)
  const channel = channelSchema.parse({
    ...multiKeyChannel,
    channel_info: { ...multiKeyChannel.channel_info, multi_key_size: 3 },
    balance_detail: {
      amount: null,
      currency: 'USD',
      unlimited: false,
      windows: [],
      keys: {
        '0': { amount: 42, currency: 'CNY', unlimited: false, windows: [] },
        '1': {
          amount: null,
          currency: 'USD',
          unlimited: false,
          windows: [{ name: 'Daily', used: 30, limit: 40 }],
          error: 'Key rejected by upstream',
        },
      },
    },
  })

  render(
    <QueryClientProvider client={queryClient}>
      <ChannelsProvider>
        <OpenDialog channel={channel} />
      </ChannelsProvider>
    </QueryClientProvider>
  )

  const first = await screen.findByRole('row', { name: /^#1\s/ })
  expect(within(first).getByText('CN¥42')).toBeInTheDocument()
  const second = screen.getByRole('row', { name: /^#2\s/ })
  expect(within(second).getByText('75% used')).toBeInTheDocument()
  expect(within(second).getByText('Daily: 30 / 40')).toBeInTheDocument()
  expect(
    within(second).getByText('Last error: Key rejected by upstream')
  ).toBeInTheDocument()
  const balanceColumn = screen
    .getAllByRole('columnheader')
    .findIndex((header) => header.textContent === 'Balance')
  const third = screen.getByRole('row', { name: /^#3\s/ })
  expect(within(third).getAllByRole('cell')[balanceColumn]).toHaveTextContent(
    /^-$/
  )
})

test('prefers current per-key currencies and windows while failed keys keep their last numeric balance', async () => {
  const monitor = statusResponse().data.data.balance_monitor
  const response = {
    balance_monitor: {
      ...monitor,
      key_balances: [
        { index: 0, balance: 10 },
        { index: 1, balance: null, error: 'non_numeric_balance' },
        { index: 2, balance: null, error: 'balance_query_failed', last_known_balance: 6 },
      ],
    },
    balance_detail: {
      keys: {
        '0': { amount: 84, currency: 'CNY' },
        '1': { windows: [{ name: 'Hourly', used: 1, limit: 4 }] },
        '2': { amount: 999, currency: 'CNY', error: 'last upstream error' },
      },
    } as Channel['balance_detail'],
  }
  vi.spyOn(api, 'post').mockImplementation(async () => statusResponse(response))
  const channel = channelSchema.parse({
    ...multiKeyChannel,
    type: 1,
    setting: JSON.stringify({ balance_recharge_multiplier: 2 }),
    balance_detail: { keys: { '0': { amount: 888, currency: 'CNY' } } },
  })
  const client = new QueryClient()
  clients.push(client)
  const view = (masked = false) => (
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <OpenDialog channel={channel} masked={masked} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  const { rerender } = render(view())
  expect(await screen.findByText('CN¥42')).toBeVisible()
  expect(keyRow(2).getByText('25% used')).toBeVisible()
  expect(keyRow(3).getByText('Query failed')).toBeVisible()
  expect(keyRow(3).getByText('Last known balance: $3')).toBeVisible()
  expect(screen.queryByText(/last upstream error/)).not.toBeInTheDocument()

  rerender(view(true))
  expect(screen.queryByText('CN¥42')).not.toBeInTheDocument()
  expect(screen.queryByText(/Hourly/)).not.toBeInTheDocument()
  rerender(view())
  expect(await screen.findByText('CN¥42')).toBeVisible()
  await act(() => {
    originalAuth.setUser({ id: 1, username: 'reader', role: ROLE.USER })
  })
  expect(screen.queryByText('CN¥42')).not.toBeInTheDocument()
  expect(screen.queryByText('Last known balance: $3')).not.toBeInTheDocument()
})

test('null key details and changed credentials cannot reuse indexed balances from the channel list', async () => {
  const response = statusResponse()
  const detail = { keys: { '0': { amount: 444, currency: 'CNY' } } }
  let status: Record<string, unknown> = { balance_detail: null }
  vi.spyOn(api, 'post').mockImplementation(async () => statusResponse(status))
  const client = new QueryClient()
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <OpenDialog
          channel={channelSchema.parse({ ...multiKeyChannel, balance_detail: detail })}
        />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  await screen.findByText('sk-a**********0001')
  expect(keyRow(1).getByText('$0')).toBeVisible()
  expect(screen.queryByText('CN¥444')).not.toBeInTheDocument()
  status = {
    balance_detail: detail,
    balance_monitor: {
      ...response.data.data.balance_monitor,
      configuration_changed: true,
    },
  }
  await act(() => client.invalidateQueries({ queryKey: ['channels'] }))
  expect(await keyRow(1).findByText('Not queried')).toBeVisible()
  expect(screen.queryByText('CN¥444')).not.toBeInTheDocument()
})
