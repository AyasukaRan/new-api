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
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test } from 'vitest'

import { Button } from '@/components/ui/button'

import type { ChannelKeyDraft } from '../../lib/channel-form'
import { channelSchema } from '../../types'
import { ChannelKeyEditor } from '../channel-key-editor'

const keys: ChannelKeyDraft[] = [
  {
    key_identity: 'a',
    key_index: 0,
    key_preview: 'masked-a',
    groups: [],
    upstream_cost_ratio: 1.1,
    effective_upstream_cost_ratio: 1.1,
    status: 1,
  },
  {
    key_identity: 'b',
    key_index: 1,
    key_preview: 'masked-b',
    groups: [],
    upstream_cost_ratio: 0.3,
    effective_upstream_cost_ratio: 0.3,
    status: 1,
  },
  {
    key_identity: 'c',
    key_index: 2,
    key_preview: 'masked-c',
    groups: [],
    upstream_cost_ratio: null,
    effective_upstream_cost_ratio: null,
    status: 1,
  },
]
function Harness(props: { saved?: boolean }) {
  const [value, setValue] = useState(keys)
  const [mode, setMode] = useState<'random' | 'polling' | 'failover'>(
    'failover'
  )
  const [pendingKeys, setPendingKeys] = useState('')
  const [channelCost, setChannelCost] = useState<number | null>(null)
  const channel = props.saved
    ? channelSchema.parse({
        id: 42,
        name: 'Saved',
        key: '',
        type: 1,
        status: 1,
        created_time: 1,
        test_time: 0,
        response_time: 0,
        balance_updated_time: 0,
        models: 'test-model',
      })
    : undefined
  return (
    <>
      <ChannelKeyEditor
        value={value}
        onChange={setValue}
        mode={mode}
        onModeChange={setMode}
        channelGroups={['default', 'premium']}
        channelCost={channelCost}
        channel={channel}
        pendingKeys={pendingKeys}
        onPendingKeysChange={setPendingKeys}
      />
      <Button onClick={() => setChannelCost(0)}>
        Set inherited cost to zero
      </Button>
    </>
  )
}
function orderedKeys() {
  return within(screen.getByRole('list', { name: 'Key fallback order' }))
    .getAllByRole('listitem')
    .map((item) => within(item).getByText(/^masked-/).textContent)
}
function row(key: string) {
  const item = screen.getByText(`masked-${key}`).closest('li')
  if (!item) throw new Error(`Missing key ${key}`)
  return within(item)
}

test('new failover keys follow effective cost, preserve small ratios and place unknown costs last', async () => {
  const user = userEvent.setup()
  render(<Harness />)
  expect(orderedKeys()).toEqual(['masked-b', 'masked-a', 'masked-c'])
  const cost = row('a').getByRole('spinbutton', {
    name: 'Upstream cost multiplier',
  })
  await act(async () => {
    await user.clear(cost)
    await user.type(cost, '0.001')
  })
  expect(orderedKeys()).toEqual(['masked-a', 'masked-b', 'masked-c'])
  expect(row('a').getByText('Effective cost: 0.001×')).toBeVisible()
  await user.click(
    screen.getByRole('button', { name: 'Set inherited cost to zero' })
  )
  expect(orderedKeys()).toEqual(['masked-c', 'masked-a', 'masked-b'])
  expect(row('c').getByText('Effective cost: 0×')).toBeVisible()
})

test('saved order and keyboard reordering stay manual until sorting by cost is explicitly requested', async () => {
  const user = userEvent.setup()
  render(<Harness saved />)
  expect(orderedKeys()).toEqual(['masked-a', 'masked-b', 'masked-c'])
  const handle = screen.getByRole('button', { name: 'Drag Key #3 to reorder' })
  handle.focus()
  await user.keyboard('{ArrowUp}{ArrowUp}')
  expect(orderedKeys()).toEqual(['masked-c', 'masked-a', 'masked-b'])
  await user.click(
    screen.getByRole('button', { name: 'Set inherited cost to zero' })
  )
  expect(orderedKeys()).toEqual(['masked-c', 'masked-a', 'masked-b'])
  await user.click(screen.getByRole('button', { name: 'Sort keys by cost' }))
  expect(orderedKeys()).toEqual(['masked-c', 'masked-b', 'masked-a'])
})

test('key removal requires confirmation and enabled changes remain local channel drafts', async () => {
  const user = userEvent.setup()
  render(<Harness saved />)
  const enabled = row('a').getByRole('switch', { name: 'Enabled' })
  await user.click(enabled)
  expect(enabled).not.toBeChecked()
  expect(
    row('a').getByRole('spinbutton', { name: 'Upstream cost multiplier' })
  ).toHaveValue(1.1)
  await user.click(screen.getByRole('button', { name: 'Remove Key #1' }))
  const dialog = screen.getByRole('alertdialog')
  expect(
    within(dialog).getByText(
      'This key will be removed when you save the channel.'
    )
  ).toBeVisible()
  await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
  expect(orderedKeys()).toEqual(['masked-b', 'masked-c'])
})
