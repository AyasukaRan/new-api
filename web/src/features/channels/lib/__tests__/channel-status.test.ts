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
import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { getChannels } from '../../api'
import {
  channelsQueryKeys,
  handleDisableChannel,
  handleEnableChannel,
  handleBatchDisable,
  handleBatchEnable,
  handleDisableTagChannels,
  handleEnableTagChannels,
} from '../channel-actions'

const clients: QueryClient[] = []
afterEach(() => {
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

it.each([
  {
    action: 'disable',
    before: 1,
    after: 2,
    run: (client: QueryClient) => handleDisableChannel(29, client),
  },
  {
    action: 'enable',
    before: 2,
    after: 1,
    run: (client: QueryClient) => handleEnableChannel(29, client),
  },
  {
    action: 'batch disable',
    before: 1,
    after: 2,
    run: (client: QueryClient) => handleBatchDisable([29], client),
  },
  {
    action: 'batch enable',
    before: 2,
    after: 1,
    run: (client: QueryClient) => handleBatchEnable([29], client),
  },
  {
    action: 'tag disable',
    before: 1,
    after: 2,
    run: (client: QueryClient) => handleDisableTagChannels('prod', client),
  },
  {
    action: 'tag enable',
    before: 2,
    after: 1,
    run: (client: QueryClient) => handleEnableTagChannels('prod', client),
  },
])(
  '$action refreshes previously cached channel pickers and model availability',
  async (scenario) => {
    const client = new QueryClient({
      defaultOptions: { queries: { staleTime: Infinity, retry: false } },
    })
    clients.push(client)
    let status = scenario.before
    vi.spyOn(api, 'get').mockImplementation(async () => ({
      data: { success: true, data: { items: [{ id: 29, status }], total: 1 } },
    }))
    const update = async () => {
      status = scenario.after
      return { data: { success: true, data: 1 } }
    }
    vi.spyOn(api, 'post').mockImplementation(update)
    vi.spyOn(api, 'put').mockImplementation(update)
    const queries = [
      channelsQueryKeys.list({}),
      channelsQueryKeys.detail(29),
      ['channels', 'pricing-override-picker'],
      ['models', 'list'],
      ['pricing'],
      ['perf-metrics', 'example-model'],
      ['perf-metrics-summary', 24],
      ['perf-metrics-admin', 1, 100, 'example-model', 24],
      ['channel-monitoring', 29],
    ].map((queryKey) => ({ queryKey, queryFn: () => getChannels() }))
    for (const query of queries) {
      expect((await client.fetchQuery(query)).data?.items[0].status).toBe(
        scenario.before
      )
    }
    await scenario.run(client)
    for (const query of queries) {
      expect((await client.fetchQuery(query)).data?.items[0].status).toBe(
        scenario.after
      )
    }
  }
)

it('keeps the status action pending until the active channel list has refreshed', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  clients.push(client)
  let changed = false
  let release!: () => void
  const gate = new Promise<void>((resolve) => {
    release = resolve
  })
  let refetchStarted!: () => void
  const started = new Promise<void>((resolve) => {
    refetchStarted = resolve
  })
  vi.spyOn(api, 'get').mockImplementation(async () => {
    if (changed) {
      refetchStarted()
      await gate
    }
    return {
      data: {
        success: true,
        data: { items: [{ id: 29, status: changed ? 2 : 1 }], total: 1 },
      },
    }
  })
  vi.spyOn(api, 'post').mockImplementation(async () => {
    changed = true
    return { data: { success: true, data: true } }
  })
  const query = {
    queryKey: channelsQueryKeys.list({}),
    queryFn: () => getChannels(),
  }
  await client.fetchQuery(query)
  const observer = new QueryObserver(client, query)
  const unsubscribe = observer.subscribe(() => {})
  let finished = false
  const action = handleDisableChannel(29, client).then(() => {
    finished = true
  })
  try {
    await started
    await Promise.resolve()
    expect(finished).toBe(false)
  } finally {
    release()
    await action
    unsubscribe()
  }
  expect(
    client.getQueryData<Awaited<ReturnType<typeof getChannels>>>(query.queryKey)
      ?.data?.items[0].status
  ).toBe(2)
})

it('refreshes successful batch changes even when another channel fails', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  clients.push(client)
  let status = 1
  vi.spyOn(api, 'get').mockImplementation(async () => ({
    data: { success: true, data: { items: [{ id: 29, status }], total: 1 } },
  }))
  vi.spyOn(api, 'post').mockImplementation(async () => {
    status = 2
    return {
      data: {
        success: false,
        data: 1,
        message: 'Another channel was not found',
      },
    }
  })
  const query = {
    queryKey: channelsQueryKeys.list({}),
    queryFn: () => getChannels(),
  }
  await client.fetchQuery(query)
  await handleBatchDisable([29, 30], client)
  expect((await client.fetchQuery(query)).data?.items[0].status).toBe(2)
})

it('keeps the previous status when the server rejects the change', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { staleTime: Infinity, retry: false } },
  })
  clients.push(client)
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { items: [{ id: 29, status: 1 }], total: 1 } },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Failed to persist status' },
  })
  const query = {
    queryKey: channelsQueryKeys.list({}),
    queryFn: () => getChannels(),
  }
  await client.fetchQuery(query)
  const onSuccess = vi.fn()
  await handleDisableChannel(29, client, onSuccess)
  expect((await client.fetchQuery(query)).data?.items[0].status).toBe(1)
  expect(onSuccess).not.toHaveBeenCalled()
})
