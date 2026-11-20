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
  ModelDashboardMetric,
  QuotaDataItem,
  ProcessedChartData,
  ProcessedUserChartData,
} from '@/features/dashboard/types'
import { formatQuotaWithCurrency, getCurrencyDisplay } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { formatCompactNumber, formatNumber } from '@/lib/format'
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
  // The gateway filters stored UTC-hour keys, so a partial leading hour cannot be observed.
  const first =
    unit === 'hour'
      ? dayjs(Math.ceil(range.start_timestamp / 3600) * 3600 * 1000)
      : dayjs(range.start_timestamp * 1000).startOf('day')
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
  metric: ModelDashboardMetric = 'requests',
  theme: 'light' | 'dark' = 'light'
): ProcessedChartData {
  const tt: TFunction = t ?? ((x) => x)
  const otherLabel = tt('Other')
  const monetary = metric !== 'requests' && metric !== 'tokens'
  const metricLabel = {
    requests: tt('Requests'),
    tokens: tt('Tokens'),
    quota: tt('Usage'),
    cost: tt('Upstream cost'),
    profit: tt('Gross profit'),
  }[metric]
  const trendTitle =
    metric !== 'requests' ? `${metricLabel} · ${tt('Trend')}` : tt('Call Trend')
  const distributionTitle =
    metric !== 'requests' ? metricLabel : tt('Call Count Distribution')
  const rankingTitle =
    metric !== 'requests'
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

  const formatValue = (value: number) =>
    monetary
      ? formatQuotaWithCurrency(value, {
          digitsLarge: 4,
          digitsSmall: 4,
          abbreviate: false,
        })
      : formatNumber(value)
  const metricAxes = [
    {
      orient: 'bottom',
      type: 'band',
      label: { autoHide: true, autoRotate: false },
    },
    {
      orient: 'left',
      type: 'linear',
      zero: true,
      label: {
        formatMethod: (value: number) =>
          monetary
            ? formatQuotaWithCurrency(value)
            : formatCompactNumber(value),
      },
    },
  ]
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
        array[i].value = formatValue(v)
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
            value: formatValue(otherSum),
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
        value: formatValue(sum),
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
        legends: {
          visible: true,
          orient: 'top',
          position: 'end',
          maxRow: 1,
          selectMode: 'multiple',
        },
      },
      spec_area: {
        type: 'area',
        data: [{ id: 'areaData', values: [] }],
        xField: 'Time',
        yField: 'Usage',
        seriesField: 'Model',
        stack: true,
        legends: {
          visible: true,
          orient: 'top',
          position: 'end',
          maxRow: 1,
          selectMode: 'multiple',
        },
      },
      spec_model_line: {
        type: 'area',
        data: [{ id: 'lineData', values: [] }],
        xField: 'Time',
        yField: 'Count',
        seriesField: 'Model',
        legends: {
          visible: true,
          orient: 'top',
          position: 'end',
          maxRow: 1,
          selectMode: 'multiple',
        },
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
        legends: {
          visible: true,
          orient: 'top',
          position: 'end',
          maxRow: 1,
          selectMode: 'multiple',
        },
        title: {
          visible: true,
          text: rankingTitle,
        },
      },
      totalDisplay:
        metric === 'cost' || metric === 'profit'
          ? tt('Not recorded')
          : formatValue(0),
    }
  }

  const timeModelMap = new Map<string, Map<string, number>>()
  const modelTotalsMap = new Map<string, number>()
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
    // Financial inputs were normalized to their recorded metric above.
    const value =
      Number(metric === 'tokens' ? item.token_used : item.count) || 0
    let modelMap = timeModelMap.get(timeKey)
    if (!modelMap) {
      modelMap = new Map()
      timeModelMap.set(timeKey, modelMap)
    }
    modelMap.set(model, (modelMap.get(model) ?? 0) + value)
    modelTotalsMap.set(model, (modelTotalsMap.get(model) ?? 0) + value)
  })

  const sortedTimes = [...timeTimestamps]
    .sort((a, b) => a[1] - b[1])
    .map(([time]) => time)
  const ranked = [...modelTotalsMap]
    .sort(([nameA, a], [nameB, b]) => b - a || nameA.localeCompare(nameB))
    .map(([Model, Count]) => ({ Model, Count }))
  const otherTooltipColor = theme === 'dark' ? '#96a6bb' : '#61728b'
  const seriesColors =
    theme === 'dark'
      ? ['#71a6ff', '#48d5b3', '#f4b567', '#b29afa', '#7fd6ec', '#f48f9d']
      : ['#326dcb', '#087e70', '#a85a12', '#8660be', '#23788f', '#bc4e61']
  const modelColor = {
    type: 'ordinal',
    domain: [...ranked.map(({ Model }) => Model), otherLabel],
    range: [
      ...ranked.map((_, index) => seriesColors[index % seriesColors.length]),
      otherTooltipColor,
    ],
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

  const total = [...modelTotalsMap.values()].reduce(
    (sum, value) => sum + value,
    0
  )

  // The time charts and pie share the same six series and one explicit remainder.
  const displayed = ranked.slice(0, 6)
  const remainder = ranked.slice(displayed.length)
  const groups = displayed.map(({ Model }) => ({
    label: Model,
    models: [Model],
  }))
  if (remainder.length) {
    groups.push({
      label: otherLabel,
      models: remainder.map((row) => row.Model),
    })
  }
  const pieValues = displayed.map(({ Model, Count }) => ({
    type: Model,
    value: Count,
  }))
  if (remainder.length) {
    pieValues.push({
      type: otherLabel,
      value: remainder.reduce((sum, row) => sum + row.Count, 0),
    })
  }

  const modelLineValues: Array<{
    Time: string
    Model: string
    Count: number | null
  }> = []
  const distributionValues: Array<{
    Time: string
    Model: string
    rawQuota: number
    Usage: number | null
    TimeSum: number
  }> = []
  for (const time of chartTimes) {
    const values = timeModelMap.get(time)
    const timeTotal = values
      ? [...values.values()].reduce((sum, value) => sum + value, 0)
      : 0
    for (const group of groups) {
      const raw = group.models.reduce(
        (sum, model) => sum + (values?.get(model) ?? 0),
        0
      )
      const amount = hasOnlyUnrecordedCost(time, group.models) ? null : raw
      modelLineValues.push({ Time: time, Model: group.label, Count: amount })
      distributionValues.push({
        Time: time,
        Model: group.label,
        rawQuota: raw,
        Usage: amount,
        TimeSum: timeTotal,
      })
    }
  }

  const rankValues = ranked.slice(0, 20)
  if (ranked.length > rankValues.length) {
    const other = ranked
      .slice(rankValues.length)
      .reduce((sum, row) => sum + row.Count, 0)
    rankValues.push({ Model: otherLabel, Count: other })
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
      legends: { visible: true, orient: 'bottom', maxRow: 1 },
      label: { visible: false },
      color: modelColor,
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.type,
              value: (datum: Record<string, unknown>) =>
                formatValue(Number(datum?.value) || 0),
            },
            {
              key: tt('Share'),
              value: (datum: Record<string, unknown>) =>
                total > 0
                  ? `${formatNumber(((Number(datum?.value) || 0) / total) * 100)}%`
                  : '—',
            },
          ],
        },
      },
      background: { fill: 'transparent' },
      animation: true,
    },
    spec_line: {
      type: 'bar',
      data: [{ id: 'barData', values: distributionValues }],
      xField: 'Time',
      yField: 'Usage',
      axes: metricAxes,
      seriesField: 'Model',
      stack: true,
      legends: {
        visible: true,
        orient: 'top',
        position: 'end',
        maxRow: 1,
        selectMode: 'multiple',
      },
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
                formatValue(Number(datum?.rawQuota) || 0),
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
      data: [{ id: 'areaData', values: distributionValues }],
      xField: 'Time',
      yField: 'Usage',
      axes: metricAxes,
      seriesField: 'Model',
      stack: true,
      legends: {
        visible: true,
        orient: 'top',
        position: 'end',
        maxRow: 1,
        selectMode: 'multiple',
      },
      color: modelColor,
      tooltip: {
        mark: {
          content: [
            {
              key: (datum: Record<string, unknown>) => datum?.Model,
              value: (datum: Record<string, unknown>) =>
                formatValue(Number(datum?.rawQuota) || 0),
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
          curveType: 'linear',
        },
      },
      line: {
        style: {
          lineWidth: 2,
          curveType: 'linear',
        },
      },
      point: sparseTrendPoints(distributionValues, 'Model', 'Usage'),
      background: { fill: 'transparent' },
      animation: false,
    },
    spec_model_line: {
      type: 'area',
      data: [{ id: 'lineData', values: modelLineValues }],
      xField: 'Time',
      yField: 'Count',
      axes: metricAxes,
      seriesField: 'Model',
      stack: false,
      legends: {
        visible: true,
        orient: 'top',
        position: 'end',
        maxRow: 1,
        selectMode: 'multiple',
      },
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
                formatValue(Number(datum?.Count) || 0),
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
              array[i].value = formatValue(v)
            }
            array.unshift({
              key: tt('Total:'),
              value: formatValue(sum),
            })
            return array
          },
        },
      },
      area: {
        style: {
          fillOpacity: 0.08,
          curveType: 'linear',
        },
      },
      line: {
        style: {
          lineWidth: 2,
          curveType: 'linear',
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
      axes: metricAxes,
      seriesField: 'Model',
      legends: { visible: false },
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
                formatValue(Number(datum?.Count) || 0),
            },
          ],
        },
      },
      background: { fill: 'transparent' },
      // Keep bar marks aligned with the current scale after changing chart tabs.
      animation: false,
    },
    totalDisplay: formatValue(total),
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
    quota: tt('Usage'),
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
