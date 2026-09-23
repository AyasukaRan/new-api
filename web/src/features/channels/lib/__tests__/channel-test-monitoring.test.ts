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
import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  getPerfMetrics,
  getPerfMetricsSummary,
} from '@/features/performance-metrics/api'
import { api } from '@/lib/api'

import { handleTestChannel } from '../channel-actions'

const clients: QueryClient[] = []

afterEach(() => {
  for (const client of clients) client.clear()
  clients.length = 0
  vi.restoreAllMocks()
})

describe('channel tests refresh model monitoring', () => {
  it.each([
    { entry: 'direct', outcome: 'success' },
    { entry: 'direct', outcome: 'failure' },
    { entry: 'selected model', outcome: 'success' },
    { entry: 'selected model', outcome: 'failure' },
    { entry: 'selected model', outcome: 'network error' },
  ])(
    'reads fresh monitoring after a $entry test ends with $outcome',
    async ({ entry, outcome }) => {
      const client = new QueryClient({
        defaultOptions: { queries: { staleTime: 60_000, retry: false } },
      })
      clients.push(client)
      let tested = false
      const successRate = outcome === 'success' ? 100 : 0
      const summary = {
        model_name: 'example-model',
        avg_latency_ms: 250,
        avg_tps: 40,
        success_rate: successRate,
      }
      const group = {
        ...summary,
        group: 'default',
        avg_ttft_ms: 50,
        series: [],
      }
      vi.spyOn(api, 'get').mockImplementation(async (url) => {
        if (url === '/api/channel/test/12') {
          tested = true
          if (outcome === 'network error') throw new Error('network error')
          return { data: { success: outcome === 'success', time: 0.25 } }
        }
        if (url === '/api/perf-metrics/summary') {
          return {
            data: { success: true, data: { models: tested ? [summary] : [] } },
          }
        }
        if (url === '/api/perf-metrics') {
          return {
            data: {
              success: true,
              data: {
                model_name: 'example-model',
                groups: tested ? [group] : [],
              },
            },
          }
        }
        throw new Error(`Unexpected request: ${url}`)
      })
      const summaryQuery = {
        queryKey: ['perf-metrics-summary', 24],
        queryFn: () => getPerfMetricsSummary(24),
      }
      const detailQuery = {
        queryKey: ['perf-metrics', 'example-model'],
        queryFn: () => getPerfMetrics('example-model', 24),
      }
      expect((await client.fetchQuery(summaryQuery)).data.models).toEqual([])
      expect((await client.fetchQuery(detailQuery)).data.groups).toEqual([])
      client.setQueryData(['pricing'], { data: [] })

      await handleTestChannel(
        12,
        {
          silent: true,
          testModel: entry === 'direct' ? undefined : 'example-model',
        },
        undefined,
        client
      )

      expect((await client.fetchQuery(summaryQuery)).data.models).toEqual([
        summary,
      ])
      expect((await client.fetchQuery(detailQuery)).data.groups).toEqual([
        group,
      ])
      expect(client.getQueryState(['pricing'])?.isInvalidated).toBe(false)
    }
  )
})
