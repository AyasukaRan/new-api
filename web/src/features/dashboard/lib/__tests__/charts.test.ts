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
import { expect, it } from 'vitest'

import { buildChartTimeDomain, processChartData } from '../charts'

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
