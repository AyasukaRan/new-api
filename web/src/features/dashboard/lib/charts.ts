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
import { dataScheme as vchartDefaultDataScheme } from '@visactor/vchart/esm/theme/color-scheme/builtin/default'

import { MAX_CHART_TREND_POINTS } from '@/features/dashboard/constants'
import type {
  DashboardChartTimeDomain,
  DashboardMetric,
  QuotaDataItem,
  ProcessedChartData,
  ProcessedUserChartData,
} from '@/features/dashboard/types'
import { formatQuotaWithCurrency, getCurrencyDisplay } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { formatChartTime, type TimeGranularity } from '@/lib/time'

import { getDashboardMetricValue } from './finance'

type TFunction = (key: string) => string
type TooltipLineItem = {
  key: string
  value: string | number
  datum?: Record<string, unknown>
  hasShape?: boolean
  shapeType?: string
  shapeFill?: string
  shapeStroke?: string
  shapeSize?: number
}

// This is a display limit: adjacent buckets are combined, never dropped.
const MAX_TIME_BUCKETS = 512

export function buildChartTimeDomain(
  range: { start_timestamp: number; end_timestamp: number },
  granularity: TimeGranularity = 'hour'
): DashboardChartTimeDomain | undefined {
  if (
    !Number.isFinite(range.start_timestamp) ||
    !Number.isFinite(range.end_timestamp) ||
    range.end_timestamp < range.start_timestamp
  ) {
    return undefined
  }
  const unit = granularity === 'hour' ? 'hour' : 'day'
  const first = dayjs(range.start_timestamp * 1000).startOf(unit)
  const end = dayjs(range.end_timestamp * 1000)
  if (!first.isValid() || !end.isValid()) return undefined

  const baseStep = granularity === 'week' ? 7 : 1
  const unitSeconds = unit === 'hour' ? 3600 : 86400
  const estimatedCount =
    Math.floor(
      (range.end_timestamp - first.unix()) / (baseStep * unitSeconds)
    ) + 1
  const stride = Math.max(1, Math.ceil(estimatedCount / MAX_TIME_BUCKETS))
  const step = baseStep * stride
  const showYear = first.year() !== end.year()
  const dateFormat = showYear ? 'YYYY-MM-DD' : 'MM-DD'
  const labelFormat = unit === 'hour' ? `${dateFormat} HH:mm` : dateFormat
  const buckets: DashboardChartTimeDomain['buckets'] = []

  // Calendar-day addition preserves local midnight through daylight-saving changes.
  for (let index = 0; index < MAX_TIME_BUCKETS; index++) {
    const start = first.add(index * step, unit)
    if (start.unix() > range.end_timestamp) break
    let next = first.add((index + 1) * step, unit).unix()
    if (index === MAX_TIME_BUCKETS - 1) {
      next = Math.max(next, range.end_timestamp + 1)
    }
    let label = start.format(labelFormat)
    if (step > 1) {
      const last = dayjs(Math.min(next - 1, range.end_timestamp) * 1000)
      label += ` – ${last.format(labelFormat)}`
    }
    // Repeated local hours during a DST rollback must remain separate buckets.
    if (buckets.at(-1)?.label === label) label += ` ${start.format('Z')}`
    buckets.push({ start: start.unix(), end: next, label })
  }

  return { ...range, buckets }
}

export function getDashboardChartColors(domainLength: number): string[] {
  const scheme =
    vchartDefaultDataScheme.find(
      (item) => !item.maxDomainLength || domainLength <= item.maxDomainLength
    ) ?? vchartDefaultDataScheme.at(-1)

  return (scheme?.scheme ?? []).filter(
    (color): color is string => typeof color === 'string'
  )
}

function renderQuotaCompat(rawQuota: number, digits = 4): string {
  const { config, meta } = getCurrencyDisplay()
  if (meta.kind === 'tokens') return rawQuota.toLocaleString()
  const usd = rawQuota / config.quotaPerUnit
  const rate = 'exchangeRate' in meta ? meta.exchangeRate : 1
  const symbol = 'symbol' in meta ? meta.symbol : '$'
  const value = usd * rate
  const fixed = value.toFixed(digits)
  if (Number.parseFloat(fixed) === 0 && rawQuota > 0 && value > 0) {
    return symbol + Math.pow(10, -digits).toFixed(digits)
  }
  return symbol + fixed
}

// Preserve visible observations when a series has gaps or only one point.
function sparseTrendPoints(
  values: Array<Record<string, unknown>>,
  seriesField: string,
  valueField: string
) {
  const counts = new Map<unknown, number>()
  const gaps = new Set<unknown>()
  for (const value of values) {
    const series = value[seriesField]
    if (Number.isFinite(value[valueField])) {
      counts.set(series, (counts.get(series) ?? 0) + 1)
    } else {
      gaps.add(series)
    }
  }
  const visibleSeries = new Set(
    [...counts]
      .filter(([series, count]) => count === 1 || gaps.has(series))
      .map(([series]) => series)
  )
  return {
    visible: visibleSeries.size > 0,
    style: {
      visible: (datum: Record<string, unknown>) =>
        Number.isFinite(datum[valueField]) &&
        visibleSeries.has(datum[seriesField]),
    },
  }
}

/**
 * Process and aggregate chart data
 */
export function processChartData(
  data: QuotaDataItem[],
  timeGranularity: TimeGranularity = 'day',
  t?: TFunction,
  chartCornerRadius?: number,
  timeDomain?: DashboardChartTimeDomain,
  metric: DashboardMetric = 'requests'
): ProcessedChartData {
  const tt: TFunction = t ?? ((x) => x)
  const otherLabel = tt('Other')
  const monetary = metric !== 'requests'
  const metricLabel = {
    requests: tt('Requests'),
    quota: tt('Revenue'),
    cost: tt('Upstream cost'),
    profit: tt('Gross profit'),
  }[metric]
  const trendTitle = monetary
    ? `${metricLabel} · ${tt('Trend')}`
    : tt('Call Trend')
  const distributionTitle = monetary
    ? metricLabel
    : tt('Call Count Distribution')
  const rankingTitle = monetary
    ? `${metricLabel} · ${tt('Ranking')}`
    : tt('Call Count Ranking')
  const missingCostData =
    metric === 'cost' || metric === 'profit'
      ? data.filter((item) => getDashboardMetricValue(item, metric) === null)
      : []
  if (monetary) {
    data = data.flatMap((item) => {
      const value = getDashboardMetricValue(item, metric)
      if (value === null) return []
      return [{ ...item, count: value, quota: value }]
    })
  }

  const formatInt = (value: number) =>
    monetary
      ? formatQuotaWithCurrency(value, {
          digitsLarge: 4,
          digitsSmall: 4,
          abbreviate: false,
        })
      : Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value)
  const formatQuotaValue = (value: number) => renderQuotaCompat(value, 4)
  const formatQuotaTotal = (value: number) => renderQuotaCompat(value, 2)

  const MAX_TOOLTIP_MODELS = 15
  const isOtherTooltipKey = (key: string) =>
    key === 'Other' || key === otherLabel

  const makeTooltipDimensionUpdateContent = (options?: {
    collapseOverflow?: boolean
  }) => {
    const collapseOverflow = options?.collapseOverflow ?? true

    return (array: TooltipLineItem[]) => {
      const modelItems = array.filter((item) => !isOtherTooltipKey(item.key))
      const otherItems = array.filter((item) => isOtherTooltipKey(item.key))
      modelItems.sort((a, b) => (Number(b.value) || 0) - (Number(a.value) || 0))
      array = [...modelItems, ...otherItems]

      let sum = 0
      for (let i = 0; i < array.length; i++) {
        const v = Number(array[i].value) || 0
        if (
          array[i].datum &&
          (array[i].datum as Record<string, unknown>)?.TimeSum
        ) {
          sum =
            Number((array[i].datum as Record<string, unknown>)?.TimeSum) || sum
        }
        array[i].value = formatQuotaValue(v)
      }

      if (collapseOverflow && array.length > MAX_TOOLTIP_MODELS) {
        const visible = modelItems.slice(0, MAX_TOOLTIP_MODELS)
        const otherSum = [
          ...modelItems.slice(MAX_TOOLTIP_MODELS),
          ...otherItems,
        ].reduce((sum, item) => {
          const raw = item.datum
            ? Number((item.datum as Record<string, unknown>)?.rawQuota) || 0
            : 0
          return sum + raw
        }, 0)
        array = [
          ...visible,
          {
            key: otherLabel,
            value: formatQuotaValue(otherSum),
            hasShape: true,
            shapeType: 'square',
            shapeFill: otherTooltipColor,
            shapeStroke: otherTooltipColor,
            shapeSize: 8,
          },
        ]
      }

      array.unshift({
        key: tt('Total:'),
        value: formatQuotaValue(sum),
      })
      return array
    }
  }

  if (!data || data.length === 0) {
    return {
      spec_pie: {
        type: 'pie',
        data: [{ id: 'id0', values: [] }],
        outerRadius: 0.8,
        innerRadius: 0.5,
        padAngle: 0.6,
        valueField: 'value',
        categoryField: 'type',
        title: {
          visible: true,
          text: distributionTitle,
          subtext: tt('No data available'),
        },
        legends: { visible: false },
        label: { visible: false },
        tooltip: {
          mark: {
            content: [],
          },
        },
      },
      spec_line: {
        type: 'bar',
        data: [{ id: 'barData', values: [] }],
        xField: 'Time',
        yField: 'Usage',
        seriesField: 'Model',
        stack: true,
        legends: { visible: true, selectMode: 'single' },
      },
      spec_area: {
        type: 'area',
        data: [{ id: 'areaData', values: [] }],
        xField: 'Time',
        yField: 'Usage',
        seriesField: 'Model',
        stack: true,
        legends: { visible: true, selectMode: 'single' },
      },
      spec_model_line: {
        type: 'area',
        data: [{ id: 'lineData', values: [] }],
        xField: 'Time',
        yField: 'Count',
        seriesField: 'Model',
        legends: { visible: true, selectMode: 'single' },
        title: {
          visible: true,
          text: trendTitle,
        },
      },
      spec_rank_bar: {
        type: 'bar',
        data: [{ id: 'rankData', values: [] }],
        xField: 'Model',
        yField: 'Count',
        seriesField: 'Model',
        legends: { visible: true, selectMode: 'single' },
        title: {
          visible: true,
          text: rankingTitle,
        },
      },
      totalQuotaDisplay:
        metric === 'cost' || metric === 'profit'
          ? tt('Not recorded')
          : formatQuotaTotal(0),
      totalCountDisplay:
        metric === 'cost' || metric === 'profit'
          ? tt('Not recorded')
          : formatInt(0),
    }
  }

  const { config } = getCurrencyDisplay()
  const quotaPerUnit = config.quotaPerUnit

  // Aggregate all metrics by time and model
  const timeModelMap = new Map<
    string,
    Map<string, { quota: number; count: number; tokens: number }>
  >()
  const modelTotalsMap = new Map<
    string,
    { quota: number; count: number; tokens: number }
  >()
  const timeTimestamps = new Map<string, number>()

  data.forEach((item) => {
    const timestamp = Number(item.created_at)
    let timeKey: string
    if (timeDomain) {
      if (
        !Number.isFinite(timestamp) ||
        timestamp < timeDomain.start_timestamp ||
        timestamp > timeDomain.end_timestamp
      ) {
        return
      }
      let low = 0
      let high = timeDomain.buckets.length - 1
      while (low <= high) {
        const middle = Math.floor((low + high) / 2)
        if (timeDomain.buckets[middle].start <= timestamp) {
          low = middle + 1
        } else {
          high = middle - 1
        }
      }
      const bucket = timeDomain.buckets[high]
      if (!bucket || timestamp >= bucket.end) return
      timeKey = bucket.label
    } else {
      timeKey = formatChartTime(timestamp, timeGranularity)
    }
    timeTimestamps.set(
      timeKey,
      Math.min(timeTimestamps.get(timeKey) ?? timestamp, timestamp)
    )
    const model = item.model_name || 'Unknown'
    const quota = Number(item.quota) || 0
    const count = Number(item.count) || 0
    const tokens = Number(item.token_used) || 0

    // Aggregate by time and model
    let modelMap = timeModelMap.get(timeKey)
    if (!modelMap) {
      modelMap = new Map()
      timeModelMap.set(timeKey, modelMap)
    }
    const existing = modelMap.get(model) || { quota: 0, count: 0, tokens: 0 }
    modelMap.set(model, {
      quota: existing.quota + quota,
      count: existing.count + count,
      tokens: existing.tokens + tokens,
    })

    // Calculate totals
    const totalExisting = modelTotalsMap.get(model) || {
      quota: 0,
      count: 0,
      tokens: 0,
    }
    modelTotalsMap.set(model, {
      quota: totalExisting.quota + quota,
      count: totalExisting.count + count,
      tokens: totalExisting.tokens + tokens,
    })
  })

  const allModels = [...modelTotalsMap.keys()]
  const sortedTimes = [...timeTimestamps]
    .sort((a, b) => a[1] - b[1])
    .map(([time]) => time)
  const sortedModels = [...allModels].sort()
  const modelColorDomain = [...new Set([...sortedModels, otherLabel])]
  const modelColorRange = getDashboardChartColors(modelColorDomain.length)
  const otherColor = modelColorRange[modelColorDomain.indexOf(otherLabel)]
  const otherTooltipColor =
    typeof otherColor === 'string' ? otherColor : '#FF8A00'
  const modelColor = {
    type: 'ordinal',
    domain: modelColorDomain,
    range: modelColorRange,
  }

  // Pad time points if too few (default 7 points)
  const MAX_TREND_POINTS = MAX_CHART_TREND_POINTS
  const fillTimePoints = (times: string[]) => {
    if (times.length >= MAX_TREND_POINTS) return times
    const lastTime = Math.max(
      ...data.map((item) => Number(item.created_at) || 0)
    )
    let intervalSec = 3600
    if (timeGranularity === 'week') {
      intervalSec = 604800
    } else if (timeGranularity === 'day') {
      intervalSec = 86400
    }
    const padded = Array.from({ length: MAX_TREND_POINTS }, (_, i) =>
      formatChartTime(
        lastTime - (MAX_TREND_POINTS - 1 - i) * intervalSec,
        timeGranularity
      )
    )
    return padded
  }
  const chartTimes = timeDomain
    ? timeDomain.buckets.map((bucket) => bucket.label)
    : fillTimePoints(sortedTimes)

  const missingCostBuckets = new Set<string>()
  for (const item of missingCostData) {
    const timestamp = Number(item.created_at)
    const bucket = timeDomain?.buckets.find(
      (entry) => timestamp >= entry.start && timestamp < entry.end
    )
    if (
      timeDomain &&
      (!bucket ||
        timestamp < timeDomain.start_timestamp ||
        timestamp > timeDomain.end_timestamp)
    ) {
      continue
    }
    const time = bucket?.label ?? formatChartTime(timestamp, timeGranularity)
    const model = item.model_name || 'Unknown'
    if (!timeModelMap.get(time)?.has(model)) {
      missingCostBuckets.add(`${time}\0${model}`)
    }
  }

  const hasOnlyUnrecordedCost = (time: string, models: string[]) =>
    models.some((model) => missingCostBuckets.has(`${time}\0${model}`)) &&
    !models.some((model) => timeModelMap.get(time)?.has(model))

  const totalTimes = [...modelTotalsMap.values()].reduce(
    (sum, x) => sum + (Number(x.count) || 0),
    0
  )
  const totalQuotaRaw = [...modelTotalsMap.values()].reduce(
    (sum, x) => sum + (Number(x.quota) || 0),
    0
  )

  // Pie chart (model call count proportion)
  const pieValues = [...modelTotalsMap.entries()]
    .map(([model, stats]) => ({
      type: model,
      value: Number(stats.count) || 0,
    }))
    .sort((a, b) => b.value - a.value)

  // Stacked bar: model quota distribution (quota -> USD)
  const lineValues: Array<{
    Time: string
    Model: string
    rawQuota: number
    Usage: number | null
    TimeSum: number
  }> = []

  chartTimes.forEach((time) => {
    let timeData = sortedModels.map((model) => {
      const stats = timeModelMap.get(time)?.get(model)
      const rawQuota = Number(stats?.quota) || 0
      const usd = rawQuota ? rawQuota / quotaPerUnit : 0
      // Match legacy frontend getQuotaWithUnit(..., 4)
      const usage = usd ? Number(usd.toFixed(4)) : 0
      return {
        Time: time,
        Model: model,
        rawQuota,
        Usage: missingCostBuckets.has(`${time}\0${model}`) ? null : usage,
        TimeSum: 0,
      }
    })

    const timeSum = timeData.reduce((sum, item) => sum + item.rawQuota, 0)
    timeData.sort((a, b) => b.rawQuota - a.rawQuota)
    timeData = timeData.map((item) => ({ ...item, TimeSum: timeSum }))
    lineValues.push(...timeData)
  })

  // Area chart: top models by quota + "Other" bucket (too many series = unreadable)
  const MAX_AREA_MODELS = 15
  const rankedQuotaModels = [...modelTotalsMap.entries()]
    .map(([model, stats]) => ({
      Model: model,
      Quota: Number(stats.quota) || 0,
    }))
    .sort((a, b) => b.Quota - a.Quota)
  const topAreaModels = new Set(
    rankedQuotaModels.slice(0, MAX_AREA_MODELS).map((m) => m.Model)
  )

  const otherAreaModels = sortedModels.filter(
    (model) => !topAreaModels.has(model)
  )
  const areaValues: typeof lineValues = []
  chartTimes.forEach((time) => {
    const buckets = new Map<string, { rawQuota: number; usage: number }>()
    const modelMap = timeModelMap.get(time)
    let timeSum = 0
    sortedModels.forEach((model) => {
      const stats = modelMap?.get(model)
      const rawQuota = Number(stats?.quota) || 0
      const usd = rawQuota ? rawQuota / quotaPerUnit : 0
      const usage = usd ? Number(usd.toFixed(4)) : 0
      timeSum += rawQuota
      const key = topAreaModels.has(model) ? model : otherLabel
      const prev = buckets.get(key) || { rawQuota: 0, usage: 0 }
      buckets.set(key, {
        rawQuota: prev.rawQuota + rawQuota,
        usage: Number((prev.usage + usage).toFixed(4)),
      })
    })
    for (const [model, vals] of buckets) {
      const sourceModels = model === otherLabel ? otherAreaModels : [model]
      areaValues.push({
        Time: time,
        Model: model,
        rawQuota: vals.rawQuota,
        Usage: hasOnlyUnrecordedCost(time, sourceModels) ? null : vals.usage,
        TimeSum: timeSum,
      })
    }
  })

  // Line chart: model call trend (top models + "Other" bucket)
  const MAX_TREND_MODELS = 20
  const rankedTrendModels = [...modelTotalsMap.entries()]
    .map(([model, stats]) => ({
      Model: model,
      Count: Number(stats.count) || 0,
    }))
    .sort((a, b) => b.Count - a.Count)
  const topTrendModels = rankedTrendModels
    .slice(0, MAX_TREND_MODELS)
    .map((item) => item.Model)
  const otherTrendModels = rankedTrendModels
    .slice(MAX_TREND_MODELS)
    .map((item) => item.Model)

  const modelLineValues: Array<{
    Time: string
    Model: string
    Count: number | null
  }> = []
  chartTimes.forEach((time) => {
    const timeData = topTrendModels.map((model) => {
      const stats = timeModelMap.get(time)?.get(model)
      return {
        Time: time,
        Model: model,
        Count: missingCostBuckets.has(`${time}\0${model}`)
          ? null
          : Number(stats?.count) || 0,
      }
    })
    if (otherTrendModels.length > 0) {
      const otherCount = otherTrendModels.reduce((sum, model) => {
        const stats = timeModelMap.get(time)?.get(model)
        return sum + (Number(stats?.count) || 0)
      }, 0)
      timeData.push({
        Time: time,
        Model: otherLabel,
        Count: hasOnlyUnrecordedCost(time, otherTrendModels)
          ? null
          : otherCount,
      })
    }
    modelLineValues.push(...timeData)
  })

  // Rank bar: model call count ranking (top 20 + "Other" bucket)
  const MAX_RANK_MODELS = 20
  const allRankValues = [...modelTotalsMap.entries()]
    .map(([model, stats]) => ({
      Model: model,
      Count: Number(stats.count) || 0,
    }))
    .sort((a, b) => b.Count - a.Count)

  let rankValues: typeof allRankValues
  if (allRankValues.length > MAX_RANK_MODELS) {
    const topModels = allRankValues.slice(0, MAX_RANK_MODELS)
    const otherCount = allRankValues
      .slice(MAX_RANK_MODELS)
      .reduce((sum, item) => sum + item.Count, 0)
    rankValues = [...topModels, { Model: otherLabel, Count: otherCount }]
  } else {
    rankValues = allRankValues
  }

  return {
    spec_pie: {
      type: 'pie',
      data: [{ id: 'id0', values: pieValues }],
      outerRadius: 0.8,
      innerRadius: 0.5,
      padAngle: 0.6,
      valueField: 'value',
      categoryField: 'type',
      pie: {
        style:
          chartCornerRadius == null ? {} : { cornerRadius: chartCornerRadius },
        state: {
          hover: { outerRadius: 0.85, stroke: '#000', lineWidth: 1 },
          selected: { outerRadius: 0.85, stroke: '#000', lineWidth: 1 },
        },
      },
      title: {
        visible: true,
        text: distributionTitle,
      },
      legends: { visible: true, orient: 'left' },
      label: { visible: true },
      color: modelColor,
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.type,
              value: (datum: Record<string, unknown>) =>
                formatInt(Number(datum?.value) || 0),
            },
          ],
        },
      },
      background: { fill: 'transparent' },
      animation: true,
    },
    spec_line: {
      type: 'bar',
      data: [{ id: 'barData', values: lineValues }],
      xField: 'Time',
      yField: 'Usage',
      seriesField: 'Model',
      stack: true,
      legends: { visible: true, selectMode: 'single' },
      color: modelColor,
      bar: {
        state: {
          hover: { stroke: '#000', lineWidth: 1 },
        },
      },
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                formatQuotaValue(Number(datum?.rawQuota) || 0),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                Number(datum?.rawQuota) || 0,
            },
          ],
          updateContent: makeTooltipDimensionUpdateContent(),
        },
      },
      background: { fill: 'transparent' },
      // Animated layout changes can leave bar marks at the previous zero position.
      animation: false,
    },
    spec_area: {
      type: 'area',
      data: [{ id: 'areaData', values: areaValues }],
      xField: 'Time',
      yField: 'Usage',
      seriesField: 'Model',
      stack: false,
      legends: { visible: true, selectMode: 'single' },
      color: modelColor,
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                formatQuotaValue(Number(datum?.rawQuota) || 0),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                Number(datum?.rawQuota) || 0,
            },
          ],
          updateContent: makeTooltipDimensionUpdateContent({
            collapseOverflow: false,
          }),
        },
      },
      area: {
        style: {
          fillOpacity: 0.08,
          curveType: 'monotone',
        },
      },
      line: {
        style: {
          lineWidth: 2,
          curveType: 'monotone',
        },
      },
      point: sparseTrendPoints(areaValues, 'Model', 'Usage'),
      background: { fill: 'transparent' },
      animation: false,
    },
    spec_model_line: {
      type: 'area',
      data: [{ id: 'lineData', values: modelLineValues }],
      xField: 'Time',
      yField: 'Count',
      axes: monetary
        ? [
            { orient: 'bottom', type: 'band' },
            {
              orient: 'left',
              type: 'linear',
              zero: true,
              label: {
                formatMethod: (value: number) => formatQuotaWithCurrency(value),
              },
            },
          ]
        : undefined,
      seriesField: 'Model',
      stack: false,
      legends: { visible: true, selectMode: 'single' },
      color: modelColor,
      title: {
        visible: true,
        text: trendTitle,
      },
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                formatInt(Number(datum?.Count) || 0),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                Number(datum?.Count) || 0,
            },
          ],
          updateContent: (
            array: Array<{
              key: string
              value: string | number
            }>
          ) => {
            const modelItems = array.filter(
              (item) => !isOtherTooltipKey(item.key)
            )
            const otherItems = array.filter((item) =>
              isOtherTooltipKey(item.key)
            )
            modelItems.sort(
              (a, b) => (Number(b.value) || 0) - (Number(a.value) || 0)
            )
            array = [...modelItems, ...otherItems]

            let sum = 0
            for (let i = 0; i < array.length; i++) {
              const v = Number(array[i].value) || 0
              sum += v
              array[i].value = formatInt(v)
            }
            array.unshift({
              key: tt('Total:'),
              value: formatInt(sum),
            })
            return array
          },
        },
      },
      area: {
        style: {
          fillOpacity: 0.08,
          curveType: 'monotone',
        },
      },
      line: {
        style: {
          lineWidth: 2,
          curveType: 'monotone',
        },
      },
      point: sparseTrendPoints(modelLineValues, 'Model', 'Count'),
      background: { fill: 'transparent' },
      animation: false,
    },
    spec_rank_bar: {
      type: 'bar',
      data: [{ id: 'rankData', values: rankValues }],
      xField: 'Model',
      yField: 'Count',
      axes: monetary
        ? [
            { orient: 'bottom', type: 'band' },
            {
              orient: 'left',
              type: 'linear',
              zero: true,
              label: {
                formatMethod: (value: number) => formatQuotaWithCurrency(value),
              },
            },
          ]
        : undefined,
      seriesField: 'Model',
      legends: { visible: true, selectMode: 'single' },
      color: modelColor,
      title: {
        visible: true,
        text: rankingTitle,
      },
      bar: {
        state: {
          hover: { stroke: '#000', lineWidth: 1 },
        },
      },
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                formatInt(Number(datum?.Count) || 0),
            },
          ],
        },
      },
      background: { fill: 'transparent' },
      // Keep bar marks aligned with the current scale after changing chart tabs.
      animation: false,
    },
    totalQuotaDisplay: formatQuotaTotal(totalQuotaRaw),
    totalCountDisplay: formatInt(totalTimes),
  }
}

const USER_COLORS = [
  '#5B8FF9',
  '#5AD8A6',
  '#F6BD16',
  '#E8684A',
  '#6DC8EC',
  '#9270CA',
  '#FF9D4D',
  '#269A99',
  '#FF99C3',
  '#5D7092',
]

export function processUserChartData(
  data: QuotaDataItem[],
  timeGranularity: TimeGranularity = 'day',
  t?: TFunction,
  limit = 10,
  metric: DashboardMetric = 'quota'
): ProcessedUserChartData {
  const tt: TFunction = t ?? ((x) => x)
  const { config } = getCurrencyDisplay()
  const quotaPerUnit = config.quotaPerUnit

  const formatVal = (raw: number) =>
    formatQuotaWithCurrency(raw, {
      digitsLarge: 4,
      digitsSmall: 4,
      abbreviate: false,
    })
  const metricLabel = {
    requests: tt('Requests'),
    quota: tt('Revenue'),
    cost: tt('Upstream cost'),
    profit: tt('Gross profit'),
  }[metric]
  const rankingTitle =
    metric === 'quota'
      ? tt('User Consumption Ranking')
      : `${metricLabel} · ${tt('Ranking')}`
  const trendTitle =
    metric === 'quota'
      ? tt('User Consumption Trend')
      : `${metricLabel} · ${tt('Trend')}`
  const missingCostData = data.filter(
    (item) => getDashboardMetricValue(item, metric) === null
  )
  data = data.flatMap((item) => {
    const value = getDashboardMetricValue(item, metric)
    return value === null ? [] : [{ ...item, quota: value }]
  })

  const emptyResult: ProcessedUserChartData = {
    spec_user_rank: {
      type: 'bar',
      data: [{ id: 'userRankData', values: [] }],
      xField: 'rawQuota',
      yField: 'User',
      seriesField: 'User',
      direction: 'horizontal',
      title: {
        visible: true,
        text: rankingTitle,
        subtext: tt('No data available'),
      },
      legends: { visible: false },
      color: { type: 'ordinal', range: USER_COLORS },
      background: { fill: 'transparent' },
    },
    spec_user_trend: {
      type: 'area',
      data: [{ id: 'userTrendData', values: [] }],
      xField: 'Time',
      yField: 'rawQuota',
      seriesField: 'User',
      title: {
        visible: true,
        text: trendTitle,
        subtext: tt('No data available'),
      },
      legends: { visible: true, selectMode: 'single' },
      color: { type: 'ordinal', range: USER_COLORS },
      point: { visible: false },
      background: { fill: 'transparent' },
    },
  }

  if (!data || data.length === 0) return emptyResult

  const userQuotaTotal = new Map<string, number>()
  data.forEach((item) => {
    const username = item.username || 'unknown'
    const prev = userQuotaTotal.get(username) || 0
    userQuotaTotal.set(username, prev + (Number(item.quota) || 0))
  })

  const sorted = [...userQuotaTotal.entries()].sort((a, b) => b[1] - a[1])
  const topUsers = sorted.slice(0, limit).map(([u]) => u)
  const topUserSet = new Set(topUsers)
  const totalQuota = sorted.slice(0, limit).reduce((s, [, q]) => s + q, 0)

  const rankValues = sorted.slice(0, limit).map(([username, quota]) => ({
    User: username,
    rawQuota: quota,
    Usage: Number((quota / quotaPerUnit).toFixed(4)),
  }))

  const userColorMap = topUsers.reduce<Record<string, string>>(
    (acc, user, i) => {
      acc[user] = USER_COLORS[i % USER_COLORS.length]
      return acc
    },
    {}
  )

  const timeUserMap = new Map<string, Map<string, number>>()
  const timeTimestamps = new Map<string, number>()

  data.forEach((item) => {
    const ts = Number(item.created_at)
    const timeKey = formatChartTime(ts, timeGranularity)
    timeTimestamps.set(timeKey, Math.min(timeTimestamps.get(timeKey) ?? ts, ts))
    const user = item.username || 'unknown'
    if (!topUserSet.has(user)) return
    let map = timeUserMap.get(timeKey)
    if (!map) {
      map = new Map()
      timeUserMap.set(timeKey, map)
    }
    map.set(user, (map.get(user) || 0) + (Number(item.quota) || 0))
  })

  const missingCostBuckets = new Set<string>()
  for (const item of missingCostData) {
    const user = item.username || 'unknown'
    if (!topUserSet.has(user)) continue
    const ts = Number(item.created_at)
    const time = formatChartTime(ts, timeGranularity)
    timeTimestamps.set(time, Math.min(timeTimestamps.get(time) ?? ts, ts))
    if (!timeUserMap.get(time)?.has(user)) {
      missingCostBuckets.add(`${time}\0${user}`)
    }
  }

  const sortedTimePoints = [...timeTimestamps]
    .sort((a, b) => a[1] - b[1])
    .map(([time]) => time)
  const trendValues: Array<{
    Time: string
    User: string
    rawQuota: number | null
    Usage: number
  }> = []

  sortedTimePoints.forEach((time) => {
    topUsers.forEach((user) => {
      const q = timeUserMap.get(time)?.get(user) || 0
      trendValues.push({
        Time: time,
        User: user,
        rawQuota: missingCostBuckets.has(`${time}\0${user}`) ? null : q,
        Usage: Number((q / quotaPerUnit).toFixed(4)),
      })
    })
  })

  return {
    spec_user_rank: {
      type: 'bar',
      data: [{ id: 'userRankData', values: rankValues }],
      xField: 'rawQuota',
      yField: 'User',
      seriesField: 'User',
      direction: 'horizontal',
      title: {
        visible: true,
        text: rankingTitle,
        subtext: `${tt('Total:')} ${formatVal(totalQuota)}`,
      },
      legends: { visible: false },
      bar: {
        state: { hover: { stroke: '#000', lineWidth: 1 } },
      },
      label: {
        visible: true,
        position: 'outside',
        formatMethod: (value: number) => formatVal(value),
        style: { fontSize: 11 },
      },
      axes: [
        { orient: 'left', type: 'band' },
        { orient: 'bottom', type: 'linear', visible: false },
      ],
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.User,
              value: (datum: Record<string, unknown>) =>
                formatVal(Number(datum?.rawQuota) || 0),
            },
          ],
          updateContent: (
            array: Array<{
              key: string
              value: string | number
              datum?: Record<string, unknown>
            }>
          ) => {
            for (let i = 0; i < array.length; i++) {
              const rawQuota = array[i].datum?.rawQuota
              const value =
                rawQuota === undefined ? array[i].value : Number(rawQuota)
              array[i].value = formatVal(Number(value) || 0)
            }
            return array
          },
        },
      },
      color: { specified: userColorMap },
      background: { fill: 'transparent' },
      // Keep bar marks aligned with the current scale after changing chart tabs.
      animation: false,
    },
    spec_user_trend: {
      type: 'area',
      data: [{ id: 'userTrendData', values: trendValues }],
      xField: 'Time',
      yField: 'rawQuota',
      seriesField: 'User',
      stack: false,
      title: {
        visible: true,
        text: trendTitle,
        subtext: `${tt('Total:')} ${formatVal(totalQuota)}`,
      },
      legends: { visible: true, selectMode: 'single' },
      axes: [
        { orient: 'bottom', type: 'band' },
        {
          orient: 'left',
          type: 'linear',
          label: {
            formatMethod: (value: number) => formatVal(value),
          },
        },
      ],
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.User,
              value: (datum: Record<string, unknown>) =>
                formatVal(Number(datum?.rawQuota) || 0),
            },
          ],
        },
        dimension: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.User,
              value: (datum: Record<string, unknown>) =>
                Number(datum?.rawQuota) || 0,
            },
          ],
          updateContent: (
            array: Array<{
              key: string
              value: string | number
            }>
          ) => {
            array.sort(
              (a, b) => (Number(b.value) || 0) - (Number(a.value) || 0)
            )
            let sum = 0
            for (let i = 0; i < array.length; i++) {
              const v = Number(array[i].value) || 0
              sum += v
              array[i].value = formatVal(v)
            }
            array.unshift({
              key: tt('Total:'),
              value: formatVal(sum),
            })
            return array
          },
        },
      },
      area: {
        style: {
          fillOpacity: 0.15,
          curveType: 'monotone',
        },
      },
      line: {
        style: {
          lineWidth: 2,
          curveType: 'monotone',
        },
      },
      point: sparseTrendPoints(trendValues, 'User', 'rawQuota'),
      color: { specified: userColorMap },
      background: { fill: 'transparent' },
      animation: false,
    },
  }
}
