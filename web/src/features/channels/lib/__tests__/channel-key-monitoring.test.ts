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
import { expect, test, vi } from 'vitest'

import type {
  ChannelBalanceSample,
  ChannelKeyUsageMonitoring,
} from '../../types'
import {
  CHANNEL_FORM_DEFAULT_VALUES,
  channelFormSchema,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
} from '../channel-form'
import {
  formatKeyUsageUSD,
  getKeyBalanceHistory,
  getKeyDailyBalanceDecrease,
  getKeyUsageTrend,
} from '../channel-key-monitoring'

const sample: ChannelBalanceSample = {
  id: 1,
  channel_id: 1,
  checked_at: 100,
  balance: 5,
  balance_updated_time: 100,
  known_balance: 5,
  partial: false,
  success: true,
  used_quota: 0,
  configuration_segment: 1,
  key_balances: [{ index: 0, balance: 5 }],
}

test('key balance history preserves zero and breaks at failures and replaced accounts without carrying a last-known balance', () => {
  const history = [
    sample,
    {
      ...sample,
      id: 2,
      checked_at: 200,
      key_balances: [{ index: 0, balance: 0 }],
    },
    {
      ...sample,
      id: 3,
      checked_at: 300,
      partial: true,
      key_balances: [
        {
          index: 0,
          balance: null,
          error: 'balance_query_failed',
          last_known_balance: 5,
        },
      ],
    },
    { ...sample, id: 4, checked_at: 400, configuration_segment: 2 },
  ]
  expect(getKeyBalanceHistory(history, 0, 2)).toEqual([
    { timestamp: 100, balance: 2.5 },
    { timestamp: 200, balance: 0 },
    { timestamp: 300, balance: null },
    { timestamp: 350, balance: null },
    { timestamp: 400, balance: 2.5 },
  ])
  expect(
    getKeyBalanceHistory(
      [
        {
          ...sample,
          key_balances: [{ index: 0, balance: 8, error: 'failed' }],
        },
      ],
      0
    )
  ).toEqual([{ timestamp: 100, balance: null }])
  expect(
    getKeyBalanceHistory(
      [
        sample,
        { ...sample, checked_at: 200, configuration_segment: undefined },
      ],
      0
    )[1]
  ).toEqual({ timestamp: 150, balance: null })
})

test('single-channel history retains complete-only balance semantics and disconnects changed configurations', () => {
  const result = getKeyBalanceHistory(
    [
      sample,
      { ...sample, checked_at: 200, partial: true },
      { ...sample, checked_at: 300, configuration_segment: 2 },
    ],
    null
  )
  expect(result.map((point) => point.balance)).toEqual([5, null, null, 5])
})

test('usage trends keep zero cost distinct from unknown cost and aggregate daily tokens for longer windows', () => {
  const row = {
    key_identity: 'a',
    input_tokens: 10,
    output_tokens: 5,
    request_count: 1,
    cost_usd: null,
    actual_cost_requests: 0,
    estimated_cost_requests: 0,
    unknown_cost_requests: 1,
    pending_requests: 0,
  }
  const data: ChannelKeyUsageMonitoring = {
    window_start: 0,
    window_end: 172800,
    keys: [],
    series: [
      { timestamp: 100, keys: [row] },
      { timestamp: 3700, keys: [{ ...row, cost_usd: 0 }] },
      { timestamp: 86500, keys: [row] },
    ],
  }
  expect(getKeyUsageTrend(data, 'a')).toEqual([
    { timestamp: 0, tokens: 30, cost: 0 },
    { timestamp: 86400, tokens: 15, cost: null },
  ])
  expect(getKeyUsageTrend(data, 'missing')).toEqual([])
  expect(
    getKeyUsageTrend(
      {
        ...data,
        window_end: 86400,
        series: [
          { timestamp: 300, keys: [row] },
          { timestamp: 600, keys: [row] },
        ],
      },
      'a'
    )
  ).toEqual([
    { timestamp: 300, tokens: 15, cost: null },
    { timestamp: 600, tokens: 15, cost: null },
  ])
  expect(formatKeyUsageUSD(0, 'en-US')).toBe('$0')
  expect(formatKeyUsageUSD(0.000001, 'en-US')).toBe('$0.000001')
  expect(formatKeyUsageUSD(0.0000000001, 'en-US')).toBe('<$0.00000001')
})

test('key weights default to one, round-trip for create and edit, and reject non-integer or out-of-range values', () => {
  const form = {
    ...CHANNEL_FORM_DEFAULT_VALUES,
    name: 'Weighted',
    models: 'test-model',
    key: '',
    multi_key_type: 'weighted_usage' as const,
    key_configs: [
      {
        key: 'new-key',
        key_identity: '',
        key_index: -1,
        key_preview: '',
        groups: [],
        upstream_cost_ratio: null,
        effective_upstream_cost_ratio: null,
        status: 1,
      },
    ],
  }
  const parsed = channelFormSchema.parse(form)
  expect(
    transformFormDataToCreatePayload(parsed).key_configs?.[0]?.routing_weight
  ).toBe(1)
  for (const weight of [1, 3, 1000]) {
    const values = channelFormSchema.parse({
      ...form,
      key_configs: [{ ...form.key_configs[0], routing_weight: weight }],
    })
    expect(transformFormDataToCreatePayload(values)).toMatchObject({
      multi_key_mode: 'weighted_usage',
      key_configs: [{ routing_weight: weight }],
    })
    expect(transformFormDataToUpdatePayload(values, 1)).toMatchObject({
      multi_key_mode: 'weighted_usage',
      key_configs: [{ routing_weight: weight }],
    })
  }
  for (const routing_weight of [
    0,
    -1,
    1001,
    1.5,
    Number.NaN,
    Number.POSITIVE_INFINITY,
  ]) {
    expect(
      channelFormSchema.safeParse({
        ...form,
        key_configs: [{ ...form.key_configs[0], routing_weight }],
      }).success
    ).toBe(false)
  }
})

test('daily balance decline ignores recharges, failures and previous configurations and requires one observed day', () => {
  const history = [100, 90, 140, 130].map((balance, index) => ({
    ...sample,
    id: index + 1,
    checked_at: 100 + index * 86400,
    key_balances: [{ index: 0, balance }],
  }))
  expect(getKeyDailyBalanceDecrease(history, 0, 2)).toBeCloseTo(10 / 3)
  expect(
    getKeyDailyBalanceDecrease(
      [history[0], { ...history[1], checked_at: 100 + 3600 }],
      0
    )
  ).toBeNull()
  expect(
    getKeyDailyBalanceDecrease(
      [history[0], { ...history[1], configuration_segment: 2 }],
      0
    )
  ).toBeNull()
  expect(
    getKeyDailyBalanceDecrease(
      [
        history[0],
        {
          ...history[1],
          key_balances: [
            {
              index: 0,
              balance: null,
              last_known_balance: 100,
              error: 'failed',
            },
          ],
        },
        history[2],
      ],
      0
    )
  ).toBeNull()
  expect(
    getKeyDailyBalanceDecrease(
      [
        history[0],
        { ...history[1], key_balances: [{ index: 0, balance: 110 }] },
      ],
      0
    )
  ).toBe(0)
})

test('daily usage buckets follow browser midnight in Asia/Shanghai rather than UTC midnight', () => {
  vi.stubEnv('TZ', 'Asia/Shanghai')
  try {
    const key = {
      key_identity: 'a',
      input_tokens: 10,
      output_tokens: 5,
      request_count: 1,
      cost_usd: 0.01,
      actual_cost_requests: 1,
      estimated_cost_requests: 0,
      unknown_cost_requests: 0,
      pending_requests: 0,
    }
    const timestamp = Date.parse('2026-11-26T16:05:00Z') / 1000
    const data: ChannelKeyUsageMonitoring = {
      window_start: timestamp - 172800,
      window_end: timestamp + 86400,
      keys: [],
      series: [
        { timestamp: timestamp - 600, keys: [key] },
        { timestamp, keys: [key] },
        { timestamp: timestamp + 3600, keys: [key] },
      ],
    }
    expect(getKeyUsageTrend(data, 'a')).toEqual([
      {
        timestamp: Date.parse('2026-11-25T16:00:00Z') / 1000,
        tokens: 15,
        cost: 0.01,
      },
      {
        timestamp: Date.parse('2026-11-26T16:00:00Z') / 1000,
        tokens: 30,
        cost: 0.02,
      },
    ])
  } finally {
    vi.unstubAllEnvs()
  }
})
