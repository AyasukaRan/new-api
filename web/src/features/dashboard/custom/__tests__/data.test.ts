import { describe, expect, it } from 'vitest'

import {
  boardTimeRange,
  buildLivePanelData,
  buildUsagePanelData,
} from '../data'
import {
  createDefaultPanel,
  getPanelCapabilities,
  isPanelAllowed,
  resolvePanelTime,
} from '../registry'
import type { LiveDashboardResponse } from '../types'

const range = { start_timestamp: 3600, end_timestamp: 10799 }
const time = { range: '6h', granularity: 'hour', refresh: 30 } as const
const finance = {
  free_quota: 2,
  cost_quota: 3,
  profit_quota: 5,
  covered_revenue_quota: 8,
  recorded_count: 1,
  actual_count: 1,
  estimated_count: 0,
  unrecorded_count: 0,
}

describe('personal dashboard metrics', () => {
  it('aggregates selected model only and keeps measured zero cache distinct from missing cache', () => {
    const panel = {
      ...createDefaultPanel(),
      metric: 'cache_read' as const,
      filters: { model: 'a' },
    }
    const data = buildUsagePanelData(
      [
        { model_name: 'a', created_at: 3600, cache_read_tokens: 0 },
        { model_name: 'a', created_at: 7200 },
        { model_name: 'b', created_at: 3600, cache_read_tokens: 500 },
      ],
      panel,
      range,
      time
    )
    expect(data.series).toEqual([{ key: 's0', label: 'a', value: null }])
    expect(data.points).toEqual([
      { time: 3600, s0: 0 },
      { time: 7200, s0: null },
    ])
  })

  it('excludes free quota from revenue and does not report incomplete ledger cost as complete', () => {
    const rows = [
      { model_name: 'a', created_at: 3600, count: 1, quota: 10, finance },
      { model_name: 'a', created_at: 3600, count: 1, quota: 5 },
    ]
    const panel = {
      ...createDefaultPanel(),
      dimension: 'total' as const,
      metric: 'quota' as const,
    }
    expect(buildUsagePanelData(rows, panel, range, time).series[0].value).toBe(
      13
    )
    expect(
      buildUsagePanelData(
        rows,
        { ...panel, metric: 'upstream_cost' },
        range,
        time
      ).series[0].value
    ).toBeNull()
    expect(
      buildUsagePanelData(
        [rows[0]],
        { ...panel, metric: 'upstream_cost' },
        range,
        time
      ).series[0].value
    ).toBe(3)
  })

  it('uses exact server window quantiles instead of averaging minute quantiles', () => {
    const point = {
      ts: 3600,
      model: '',
      requests: 100,
      success_count: 99,
      latency_ms: 123,
      ttft_ms: 30,
      tps: 20,
      p95_ms: 300,
      concurrency: 2,
    }
    const result: LiveDashboardResponse = {
      instance: 'node',
      observed_from: 3600,
      window_start: 3600,
      window_end: 7200,
      step_seconds: 60,
      active: 1,
      models_truncated: false,
      p95_limited: false,
      series: [],
      summary: [],
      total: point,
      totals: [
        { ...point, p95_ms: 100 },
        { ...point, ts: 3660, p95_ms: 2000 },
      ],
    }
    const data = buildLivePanelData(result, {
      ...createDefaultPanel('live'),
      dimension: 'total',
      metric: 'p95',
    })
    expect(data.series[0].value).toBe(300)
    expect(data.points).toEqual([
      { time: 3600, s0: 100 },
      { time: 3660, s0: 2000 },
    ])
    expect(
      buildLivePanelData(result, {
        ...createDefaultPanel('live'),
        dimension: 'total',
        metric: 'success_rate',
      }).series[0].value
    ).toBe(99)
  })

  it('uses local calendar boundaries for today and yesterday without a rolling 24 hour approximation', () => {
    const now = new Date(2026, 10, 26, 14, 20, 0)
    expect(boardTimeRange({ ...time, range: 'today' }, now.getTime())).toEqual({
      start_timestamp: new Date(2026, 10, 26).getTime() / 1000,
      end_timestamp: now.getTime() / 1000,
    })
    expect(
      boardTimeRange({ ...time, range: 'yesterday' }, now.getTime())
    ).toEqual({
      start_timestamp: new Date(2026, 10, 25).getTime() / 1000,
      end_timestamp: new Date(2026, 10, 26).getTime() / 1000 - 1,
    })
  })

  it('limits inherited settings to actual source resolution and available live retention', () => {
    expect(
      resolvePanelTime('usage', {
        range: 'today',
        granularity: 'minute',
        refresh: 5,
      })
    ).toEqual({ range: 'today', granularity: 'hour', refresh: 30 })
    expect(
      resolvePanelTime('live', { range: '30d', granularity: 'day', refresh: 5 })
    ).toEqual({ range: '24h', granularity: '5m', refresh: 5 })
  })

  it('does not offer private metrics or unsupported grouped concurrency', () => {
    expect(getPanelCapabilities('sources', false).dimensions).toEqual(['total'])
    expect(isPanelAllowed(createDefaultPanel('live'), false)).toBe(false)
    expect(
      isPanelAllowed({ ...createDefaultPanel(), metric: 'profit' }, false)
    ).toBe(false)
    expect(
      isPanelAllowed(
        { ...createDefaultPanel(), filters: { username: 'someone' } },
        false
      )
    ).toBe(false)
    expect(
      isPanelAllowed(
        {
          ...createDefaultPanel('live'),
          metric: 'concurrency',
          dimension: 'total',
          filters: { model: 'a' },
        },
        true
      )
    ).toBe(false)
  })
})
