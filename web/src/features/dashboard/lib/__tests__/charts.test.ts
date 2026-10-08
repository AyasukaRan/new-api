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
import { describe, expect, it, test } from 'vitest'

import type { QuotaDataItem } from '@/features/dashboard/types'
import type { TimeGranularity } from '@/lib/time'

import {
  buildChartTimeDomain,
  processChartData,
  processUserChartData,
} from '../charts'

function timestamp(localDate: string): number {
  return new Date(localDate).getTime() / 1000
}

type TrendPoint = { Time: string; Model: string; Count: number }
type UsagePoint = { Time: string; rawQuota: number }

it('uses the selected hourly window for sparse model and source series with different last observations', () => {
  const start = timestamp('2026-09-22T00:00:00')
  const timeDomain = buildChartTimeDomain(
    { start_timestamp: start, end_timestamp: start + 3 * 3600 },
    'hour'
  )
  const models = processChartData(
    [{ model_name: 'deepseek-flash', created_at: start, count: 4, quota: 100 }],
    'hour',
    undefined,
    undefined,
    timeDomain
  )
  const sources = processChartData(
    [
      {
        model_name: 'Python Requests',
        created_at: start + 2 * 3600,
        count: 3,
        quota: 75,
      },
    ],
    'hour',
    undefined,
    undefined,
    timeDomain
  )
  const modelPoints = models.spec_model_line.data[0].values as TrendPoint[]
  const sourcePoints = sources.spec_model_line.data[0].values as TrendPoint[]

  expect(modelPoints.map((point) => point.Time)).toEqual([
    '09-22 00:00',
    '09-22 01:00',
    '09-22 02:00',
    '09-22 03:00',
  ])
  expect(sourcePoints.map((point) => point.Time)).toEqual(
    modelPoints.map((point) => point.Time)
  )
  expect(modelPoints.map((point) => point.Count)).toEqual([4, 0, 0, 0])
  expect(sourcePoints.map((point) => point.Count)).toEqual([0, 0, 3, 0])
  for (const spec of [models.spec_line, models.spec_area]) {
    const points = spec.data[0].values as UsagePoint[]
    expect(points.map((point) => point.Time)).toEqual(
      modelPoints.map((point) => point.Time)
    )
    expect(points.reduce((sum, point) => sum + point.rawQuota, 0)).toBe(100)
  }
})

it('aggregates hourly requests into shared daily and weekly buckets without losing totals', () => {
  const start = timestamp('2026-09-21T00:00:00')
  const end = timestamp('2026-09-29T23:59:59')
  const data = [
    {
      model_name: 'Python Requests',
      created_at: start + 3600,
      count: 2,
      quota: 10,
    },
    {
      model_name: 'Python Requests',
      created_at: start + 2 * 3600,
      count: 3,
      quota: 20,
    },
    {
      model_name: 'Python Requests',
      created_at: timestamp('2026-09-28T12:00:00'),
      count: 4,
      quota: 30,
    },
  ]
  const range = { start_timestamp: start, end_timestamp: end }
  const daily = processChartData(
    data,
    'day',
    undefined,
    undefined,
    buildChartTimeDomain(range, 'day')
  )
  const weekly = processChartData(
    data,
    'week',
    undefined,
    undefined,
    buildChartTimeDomain(range, 'week')
  )
  const dailyPoints = daily.spec_model_line.data[0].values as TrendPoint[]
  const weeklyPoints = weekly.spec_model_line.data[0].values as TrendPoint[]
  expect(dailyPoints).toHaveLength(9)
  expect(
    dailyPoints.filter((point) => point.Count).map((point) => point.Count)
  ).toEqual([5, 4])
  expect(weeklyPoints.map((point) => point.Count)).toEqual([5, 4])
  expect(weekly.totalCountDisplay).toBe('9')
  expect(
    (weekly.spec_area.data[0].values as UsagePoint[]).reduce(
      (sum, point) => sum + point.rawQuota,
      0
    )
  ).toBe(60)
})

it('keeps cross-year buckets chronological and distinct rather than sorting month labels', () => {
  const start = timestamp('2026-12-31T23:00:00')
  const end = timestamp('2027-01-01T01:00:00')
  const data = [
    { model_name: 'Codex CLI', created_at: start, count: 2, quota: 10 },
    { model_name: 'Codex CLI', created_at: end, count: 5, quota: 20 },
  ]
  const chart = processChartData(
    data,
    'hour',
    undefined,
    undefined,
    buildChartTimeDomain({ start_timestamp: start, end_timestamp: end }, 'hour')
  )
  const trend = chart.spec_model_line.data[0].values as TrendPoint[]
  expect(trend.map((point) => point.Count)).toEqual([2, 0, 5])
  expect(trend[0].Time).toContain('2026-12-31')
  expect(trend[2].Time).toContain('2027-01-01')
  expect(
    (chart.spec_line.data[0].values as UsagePoint[]).map((point) => point.Time)
  ).toEqual(trend.map((point) => point.Time))
})

it('bounds a multi-year hourly chart by combining adjacent buckets and preserving every request and quota', () => {
  const start = timestamp('2025-01-01T00:00:00')
  const middle = timestamp('2026-01-01T00:00:00')
  const end = timestamp('2027-01-01T00:00:00')
  const data = [
    { model_name: 'Codex CLI', created_at: start, count: 2, quota: 10 },
    { model_name: 'Codex CLI', created_at: middle, count: 3, quota: 20 },
    { model_name: 'Codex CLI', created_at: end, count: 5, quota: 30 },
  ]
  const chart = processChartData(
    data,
    'hour',
    undefined,
    undefined,
    buildChartTimeDomain({ start_timestamp: start, end_timestamp: end }, 'hour')
  )
  const trend = chart.spec_model_line.data[0].values as TrendPoint[]
  expect(trend.length).toBeLessThanOrEqual(512)
  expect(trend.reduce((sum, point) => sum + point.Count, 0)).toBe(10)
  expect(
    trend.filter((point) => point.Count).map((point) => point.Count)
  ).toEqual([2, 3, 5])
  expect(new Set(trend.map((point) => point.Time)).size).toBe(trend.length)
  expect(
    (chart.spec_area.data[0].values as UsagePoint[]).reduce(
      (sum, point) => sum + point.rawQuota,
      0
    )
  ).toBe(60)
})

const cases: Array<{
  granularity: TimeGranularity
  start: string
  interval: number
  labels: string[]
}> = [
  {
    granularity: 'hour',
    start: '2025-12-31T20:00:00',
    interval: 3600,
    labels: [
      '12-31 20:00',
      '12-31 21:00',
      '12-31 22:00',
      '12-31 23:00',
      '01-01 00:00',
      '01-01 01:00',
      '01-01 02:00',
      '01-01 03:00',
    ],
  },
  {
    granularity: 'day',
    start: '2025-12-27T12:00:00',
    interval: 86400,
    labels: [
      '12-27',
      '12-28',
      '12-29',
      '12-30',
      '12-31',
      '01-01',
      '01-02',
      '01-03',
    ],
  },
  {
    granularity: 'week',
    start: '2025-11-29T12:00:00',
    interval: 604800,
    labels: [
      '11-29 - 12-05',
      '12-06 - 12-12',
      '12-13 - 12-19',
      '12-20 - 12-26',
      '12-27 - 01-02',
      '01-03 - 01-09',
      '01-10 - 01-16',
      '01-17 - 01-23',
    ],
  },
]

function unorderedUsage(start: string, interval: number): QuotaDataItem[] {
  const first = new Date(start).getTime() / 1000
  const rows = Array.from({ length: 8 }, (_, i) => ({
    created_at: first + i * interval,
    model_name: 'model-a',
    username: 'alice',
    quota: (i + 1) * 500000,
    count: i + 1,
  }))
  // Same hour, or a second hourly record within the same day/week label.
  const duplicate = {
    ...rows[0],
    created_at: first + (interval === 3600 ? 0 : 3600),
    quota: 5000000,
    count: 10,
  }
  return [
    rows[7],
    rows[2],
    duplicate,
    rows[5],
    rows[0],
    rows[6],
    rows[1],
    rows[4],
    rows[3],
  ]
}

describe.each(cases)('dashboard $granularity chart chronology', (scenario) => {
  test('orders model buckets across New Year while aggregating repeated labels', () => {
    const data = unorderedUsage(scenario.start, scenario.interval)
    const original = structuredClone(data)
    const result = processChartData(data, scenario.granularity)

    for (const key of ['spec_line', 'spec_area', 'spec_model_line'] as const) {
      const values: Array<{ Time: string; rawQuota: number; Count: number }> =
        result[key].data[0].values
      expect(values.map((row) => row.Time)).toEqual(scenario.labels)
      expect(
        values.map((row) =>
          key === 'spec_model_line' ? row.Count : row.rawQuota / 500000
        )
      ).toEqual([11, 2, 3, 4, 5, 6, 7, 8])
    }
    expect(data).toEqual(original)
  })

  test('orders user buckets across New Year while aggregating repeated labels', () => {
    const data = unorderedUsage(scenario.start, scenario.interval)
    const original = structuredClone(data)
    const result = processUserChartData(data, scenario.granularity)
    const values: Array<{ Time: string; rawQuota: number }> =
      result.spec_user_trend.data[0].values

    expect(values.map((row) => row.Time)).toEqual(scenario.labels)
    expect(values.map((row) => row.rawQuota / 500000)).toEqual([
      11, 2, 3, 4, 5, 6, 7, 8,
    ])
    expect(data).toEqual(original)
  })

  test('keeps the existing seven padded model buckets in chronological order', () => {
    const data = [unorderedUsage(scenario.start, scenario.interval)[0]]
    const result = processChartData(data, scenario.granularity)

    for (const key of ['spec_line', 'spec_area', 'spec_model_line'] as const) {
      const values: Array<{ Time: string; rawQuota: number; Count: number }> =
        result[key].data[0].values
      expect(values.map((row) => row.Time)).toEqual(scenario.labels.slice(1))
      expect(
        values.map((row) =>
          key === 'spec_model_line' ? row.Count : row.rawQuota / 500000
        )
      ).toEqual([0, 0, 0, 0, 0, 0, 8])
    }
  })
})
