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
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterContextProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { channelSchema } from '../../types'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { ChannelTestDialog } from '../dialogs/channel-test-dialog'

const clients: QueryClient[] = []
afterEach(() => {
  cleanup()
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
})

function TestDialogHarness({ model }: { model: string }) {
  const { setCurrentRow } = useChannels()
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type='button'
        onClick={() => {
          setCurrentRow(
            channelSchema.parse({
              id: 42,
              type: 1,
              key: '',
              status: 2,
              name: 'Disabled test channel',
              created_time: 1,
              test_time: 0,
              response_time: 0,
              balance_updated_time: 0,
              models: model,
            })
          )
          setOpen(true)
        }}
      >
        Open monitor
      </button>
      <ChannelTestDialog open={open} onOpenChange={setOpen} />
    </>
  )
}

function renderTestDialog(model: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  const router = createRouter({
    routeTree: createRootRoute(),
    history: createMemoryHistory(),
  })
  render(
    <RouterContextProvider router={router}>
      <QueryClientProvider client={client}>
        <ChannelsProvider>
          <TestDialogHarness model={model} />
        </ChannelsProvider>
      </QueryClientProvider>
    </RouterContextProvider>
  )
}

test('manual tests start in stream mode and preserve switching to nonstream', async () => {
  const request = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, time: 0.25, data: { tested: 1, succeeded: 1 } },
  })
  const user = userEvent.setup()
  renderTestDialog('gpt-5.5')
  await user.click(screen.getByRole('button', { name: 'Open monitor' }))
  const toggle = screen.getByRole('switch', { name: 'Stream Mode' })
  expect(toggle).toBeChecked()
  await user.click(toggle)
  expect(toggle).not.toBeChecked()
  await user.click(screen.getByRole('button', { name: 'Test Connection' }))
  await waitFor(() => expect(screen.getByText('Success')).toBeVisible())
  expect(request).toHaveBeenCalledWith(
    '/api/channel/test/42',
    expect.objectContaining({ params: { model: 'gpt-5.5', stream: false } })
  )
})

test('excluded image models show skipped and cannot be selected as failed', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      time: 0,
      data: { tested: 0, succeeded: 0, failed: 0, skipped: 1 },
    },
  })
  const user = userEvent.setup()
  renderTestDialog('gpt-image-2')
  await user.click(screen.getByRole('button', { name: 'Open monitor' }))
  await user.click(screen.getByRole('button', { name: 'Test Connection' }))
  expect(await screen.findByText('Skipped')).toBeVisible()
  expect(
    screen.getByText('Image generation models are excluded from health checks')
  ).toBeVisible()
  expect(screen.queryByText('Failed', { exact: true })).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Delete failed models' })
  ).not.toBeInTheDocument()
})

test('nonstream endpoints temporarily disable streaming and restore the selected mode for chat', async () => {
  const user = userEvent.setup()
  renderTestDialog('gpt-5.5')
  await user.click(screen.getByRole('button', { name: 'Open monitor' }))
  await user.click(screen.getByRole('combobox', { name: 'Endpoint Type' }))
  await user.click(
    screen.getByRole('option', { name: 'Embeddings (/v1/embeddings)' })
  )
  expect(screen.getByRole('switch', { name: 'Stream Mode' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.getByRole('switch', { name: 'Stream Mode' })).not.toBeChecked()
  await user.click(screen.getByRole('combobox', { name: 'Endpoint Type' }))
  await user.click(
    screen.getByRole('option', { name: 'OpenAI (/v1/chat/completions)' })
  )
  expect(
    screen.getByRole('switch', { name: 'Stream Mode' })
  ).not.toHaveAttribute('aria-disabled', 'true')
  expect(screen.getByRole('switch', { name: 'Stream Mode' })).toBeChecked()
})
