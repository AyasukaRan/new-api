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
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { ChannelsProvider } from '../channels-provider'
import { GroupKeyRoutingDialog } from '../dialogs/group-key-routing-dialog'

const clients: QueryClient[] = []
const originalAuth = useAuthStore.getState().auth
afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.setState({ auth: originalAuth })
})

function setup(canWrite = true) {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: {
        id: 1,
        username: 'operator',
        role: canWrite ? ROLE.SUPER_ADMIN : ROLE.ADMIN,
      },
    },
  })
  const state = {
    revision: 'revision-1',
    failSave: false,
    items: [
      {
        channel_id: 1,
        channel_name: 'Channel A',
        key_index: 0,
        key_identity: 'a-1',
        key_preview: 'a-***1',
        channel_priority: 100,
        priority: null as number | null,
        effective_priority: 100,
        status: 1,
      },
      {
        channel_id: 1,
        channel_name: 'Channel A',
        key_index: 1,
        key_identity: 'a-2',
        key_preview: 'a-***2',
        channel_priority: 100,
        priority: null as number | null,
        effective_priority: 100,
        status: 1,
      },
      {
        channel_id: 2,
        channel_name: 'Channel B',
        key_index: 0,
        key_identity: 'b-1',
        key_preview: 'b-***1',
        channel_priority: 50,
        priority: 80 as number | null,
        effective_priority: 80,
        status: 1,
      },
      {
        channel_id: 1,
        channel_name: 'Channel A',
        key_index: 2,
        key_identity: 'a-3',
        key_preview: 'a-***3',
        channel_priority: 100,
        priority: 0 as number | null,
        effective_priority: 0,
        status: 2,
      },
    ],
  }
  const get = vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (url === '/api/group/') {
      return { data: { success: true, data: ['DS', 'backup'] } }
    }
    if (url === '/api/channel/group_key_routing') {
      const group = config?.params?.group
      return {
        data: {
          success: true,
          data: {
            group,
            revision: state.revision,
            items:
              group === 'DS' ? state.items.map((item) => ({ ...item })) : [],
          },
        },
      }
    }
    throw new Error('Unexpected preview endpoint')
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    if (state.failSave) {
      return { data: { success: false, message: 'Operation failed' } }
    }
    const request = body as {
      items: {
        channel_id: number
        key_identity: string
        priority: number | null
      }[]
    }
    for (const change of request.items) {
      const row = state.items.find(
        (item) =>
          item.channel_id === change.channel_id &&
          item.key_identity === change.key_identity
      )
      if (!row) throw new Error('Unexpected key identity')
      row.priority = change.priority
      row.effective_priority = change.priority ?? row.channel_priority
    }
    state.revision = 'revision-2'
    return {
      data: {
        success: true,
        data: {
          group: 'DS',
          revision: state.revision,
          items: state.items.map((item) => ({ ...item })),
        },
      },
    }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <GroupKeyRoutingDialog open onOpenChange={() => undefined} />
      </ChannelsProvider>
    </QueryClientProvider>
  )
  return { state, client, get, put, user: userEvent.setup() }
}

function tier(channel: string, index: number) {
  return screen.getByRole('textbox', {
    name: `Fallback tier for ${channel} key #${index}`,
  })
}

test('group tiers activate the displayed ranks without edits and later save only changed keys', async () => {
  const { put, get, user } = setup()
  await screen.findByText('a-***1')
  expect(tier('Channel A', 1)).toHaveValue('1')
  expect(tier('Channel A', 2)).toHaveValue('1')
  expect(tier('Channel B', 1)).toHaveValue('2')
  expect(tier('Channel A', 3)).toHaveValue('3')
  expect(
    screen.getByText('Retries follow the global retry limit.')
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save tiers' })).toBeEnabled()
  await user.click(screen.getByRole('button', { name: 'Save tiers' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/group_key_routing',
      {
        group: 'DS',
        revision: 'revision-1',
        items: [
          { channel_id: 1, key_identity: 'a-1', priority: 0 },
          { channel_id: 1, key_identity: 'a-2', priority: 0 },
          { channel_id: 2, key_identity: 'b-1', priority: -1 },
          { channel_id: 1, key_identity: 'a-3', priority: -2 },
        ],
      },
      expect.anything()
    )
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save tiers' })).toBeDisabled()
  )
  expect(tier('Channel A', 3)).toHaveValue('3')
  await user.clear(tier('Channel A', 3))
  await user.type(tier('Channel A', 3), '2')
  await user.click(screen.getByRole('button', { name: 'Save tiers' }))
  await waitFor(() =>
    expect(put).toHaveBeenLastCalledWith(
      '/api/channel/group_key_routing',
      {
        group: 'DS',
        revision: 'revision-2',
        items: [{ channel_id: 1, key_identity: 'a-3', priority: -1 }],
      },
      expect.anything()
    )
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save tiers' })).toBeDisabled()
  )
  expect(tier('Channel A', 3)).toHaveValue('2')
  expect(
    get.mock.calls.every(
      ([url]) =>
        url === '/api/group/' || url === '/api/channel/group_key_routing'
    )
  ).toBe(true)
  expect(screen.getByRole('table').parentElement).toHaveClass('overflow-x-auto')
})

test('restoring channel priority confirms the group-wide reset and only clears overrides', async () => {
  const { put, user } = setup()
  await screen.findByText('a-***1')
  await user.click(
    screen.getByRole('button', { name: 'Restore channel priorities' })
  )
  expect(put).not.toHaveBeenCalled()
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Restore',
    })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/channel/group_key_routing',
      {
        group: 'DS',
        revision: 'revision-1',
        items: [
          { channel_id: 2, key_identity: 'b-1', priority: null },
          { channel_id: 1, key_identity: 'a-3', priority: null },
        ],
      },
      expect.anything()
    )
  )
})

test('failed tier saving retains drafts but a changed revision requires explicit reload', async () => {
  const { state, client, put, user } = setup()
  state.failSave = true
  await screen.findByText('a-***1')
  await user.clear(tier('Channel A', 3))
  await user.type(tier('Channel A', 3), '2')
  await user.click(screen.getByRole('button', { name: 'Save tiers' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save tiers' })).toBeEnabled()
  )
  expect(tier('Channel A', 3)).toHaveValue('2')
  state.revision = 'concurrent-change'
  await act(async () => {
    await client.invalidateQueries({ queryKey: ['channels'] })
  })
  expect(
    await screen.findByText('Routing settings changed. Reload before saving.')
  ).toBeVisible()
  expect(screen.getByRole('button', { name: 'Save tiers' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Reload routing' }))
  await waitFor(() => expect(tier('Channel A', 3)).toHaveValue('3'))
})

test('group switches keep separate drafts and account changes discard them', async () => {
  const { user } = setup()
  await screen.findByText('a-***1')
  await user.clear(tier('Channel A', 3))
  await user.type(tier('Channel A', 3), '2')
  await user.click(screen.getByRole('combobox', { name: 'Group' }))
  await user.click(screen.getByRole('option', { name: 'backup', exact: true }))
  expect(
    await screen.findByText('No eligible keys in this group')
  ).toBeVisible()
  await user.click(screen.getByRole('combobox', { name: 'Group' }))
  await user.click(screen.getByRole('option', { name: 'DS', exact: true }))
  await screen.findByText('a-***1')
  expect(tier('Channel A', 3)).toHaveValue('2')
  await act(() =>
    useAuthStore.setState({
      auth: {
        ...originalAuth,
        user: { id: 2, username: 'another-root', role: ROLE.SUPER_ADMIN },
      },
    })
  )
  await screen.findByText('a-***1')
  expect(tier('Channel A', 3)).toHaveValue('3')
})

test('tier editing requires sensitive write permission and rejects non-positive or fractional tiers', async () => {
  const { user, put } = setup(false)
  await screen.findByText('a-***1')
  expect(tier('Channel A', 1)).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Restore channel priorities' })
  ).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Save tiers' })).toBeDisabled()
  await act(() =>
    useAuthStore.setState({
      auth: {
        ...originalAuth,
        user: { id: 1, username: 'operator', role: ROLE.SUPER_ADMIN },
      },
    })
  )
  await screen.findByText('a-***1')
  for (const value of ['0', '1.5', '2147483649']) {
    await user.clear(tier('Channel A', 1))
    await user.type(tier('Channel A', 1), value)
    expect(tier('Channel A', 1)).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Save tiers' })).toBeDisabled()
  }
  expect(put).not.toHaveBeenCalled()
})
