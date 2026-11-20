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
import { useTranslation } from 'react-i18next'
import {
  Area,
  Bar,
  CartesianGrid,
  ComposedChart,
  Line,
  XAxis,
  YAxis,
} from 'recharts'

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { toIntlLocale } from '@/i18n/languages'
import {
  formatCompactNumber,
  formatLogQuota,
  formatNumber,
  formatQuota,
} from '@/lib/format'
import type { TimeGranularity } from '@/lib/time'

import type {
  PersonalUsageMetric,
  UsageSeries,
} from '../../hooks/use-personal-usage'

type ChartMetric = PersonalUsageMetric | 'cache' | 'unitCharge'

export function UsageTimeChart(props: {
  points: UsageSeries['points']
  granularity: TimeGranularity
  metric: ChartMetric
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { metric, points } = props
  const hourly = props.granularity === 'hour'
  const spansMultipleDays =
    points.length > 1 && (points.at(-1)?.time ?? 0) - points[0].time >= 86400
  const date = new Intl.DateTimeFormat(
    locale,
    hourly
      ? {
          ...(spansMultipleDays &&
            ({ month: 'short', day: 'numeric' } as const)),
          hour: '2-digit',
          minute: '2-digit',
          hourCycle: 'h23',
        }
      : { month: 'short', day: 'numeric' }
  )
  const fullDate = new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    ...(hourly && {
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23' as const,
      timeZoneName: 'short' as const,
    }),
  })
  const series = {
    requests: { label: t('Requests'), color: 'var(--chart-2)' },
    tokens: { label: t('Tokens'), color: 'var(--chart-1)' },
    usage: { label: t('Billed usage'), color: 'var(--chart-3)' },
    unitCharge: { label: t('Usage per request'), color: 'var(--chart-3)' },
    cacheRead: { label: t('Cache Read'), color: 'var(--chart-2)' },
    cacheWrite: { label: t('Cache Write'), color: 'var(--chart-4)' },
  }
  let formatTick = (value: number) => formatCompactNumber(value, locale)
  let formatValue = (value: number) => formatNumber(value, locale)
  if (metric === 'usage') formatTick = formatValue = formatQuota
  if (metric === 'unitCharge') formatTick = formatValue = formatLogQuota
  return (
    <ChartContainer config={series} className='aspect-auto h-44 w-full sm:h-48'>
      <ComposedChart
        data={points}
        accessibilityLayer
        title={metric === 'cache' ? t('Cache') : series[metric].label}
        margin={{ top: 6, left: 0, right: 6, bottom: 0 }}
      >
        <CartesianGrid vertical={false} />
        <XAxis
          dataKey='time'
          tickLine={false}
          axisLine={false}
          tickMargin={8}
          minTickGap={22}
          tickFormatter={(value: number) => date.format(value * 1000)}
        />
        <YAxis
          tickLine={false}
          axisLine={false}
          width='auto'
          tickCount={4}
          tickMargin={7}
          allowDecimals={metric === 'usage' || metric === 'unitCharge'}
          tickFormatter={formatTick}
        />
        <ChartTooltip
          content={
            <ChartTooltipContent
              labelFormatter={(_, payload) => {
                const time = payload?.[0]?.payload?.time
                return typeof time === 'number'
                  ? fullDate.format(time * 1000)
                  : undefined
              }}
              formatter={(value, name) => (
                <>
                  <span>{name}</span>
                  <strong className='ml-auto pl-3 tabular-nums'>
                    {formatValue(Number(value))}
                  </strong>
                </>
              )}
            />
          }
        />
        {metric === 'requests' && (
          <Bar
            dataKey={metric}
            name={series[metric].label}
            fill={series[metric].color}
            maxBarSize={26}
            isAnimationActive={false}
          />
        )}
        {(metric === 'tokens' || metric === 'usage') && (
          <Area
            type='linear'
            dataKey={metric}
            name={series[metric].label}
            stroke={series[metric].color}
            fill={series[metric].color}
            fillOpacity={0.14}
            strokeWidth={2}
            isAnimationActive={false}
          />
        )}
        {metric === 'unitCharge' && (
          <Line
            type='linear'
            dataKey={(point: UsageSeries['points'][number]) =>
              point.requests > 0 ? point.usage / point.requests : null
            }
            name={series.unitCharge.label}
            stroke={series.unitCharge.color}
            dot={{ r: 2 }}
            strokeWidth={2}
            isAnimationActive={false}
          />
        )}
        {metric === 'cache' &&
          (['cacheRead', 'cacheWrite'] as const).map((key) => (
            <Line
              key={key}
              type='linear'
              dataKey={key}
              name={series[key].label}
              stroke={series[key].color}
              strokeWidth={2}
              strokeDasharray={key === 'cacheWrite' ? '5 3' : undefined}
              dot={false}
              isAnimationActive={false}
            />
          ))}
      </ComposedChart>
    </ChartContainer>
  )
}
