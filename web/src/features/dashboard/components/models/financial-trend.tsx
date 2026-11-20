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
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import type { hourlyDashboardFinance } from '@/features/dashboard/lib/finance'
import { toIntlLocale } from '@/i18n/languages'
import { formatLogQuota, formatQuota } from '@/lib/format'

const series = ['revenue', 'cost', 'profit'] as const

export function FinancialTrend(props: {
  points: ReturnType<typeof hourlyDashboardFinance>
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const config = {
    revenue: { label: t('Revenue'), color: 'var(--chart-1)' },
    cost: { label: t('Upstream cost'), color: 'var(--chart-4)' },
    profit: { label: t('Gross profit'), color: 'var(--chart-2)' },
  }
  const axis = new Intl.DateTimeFormat(
    locale,
    props.points.length > 24
      ? { month: 'short', day: 'numeric', hour: '2-digit', hourCycle: 'h23' }
      : { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
  )
  const date = new Intl.DateTimeFormat(locale, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
    timeZoneName: 'short',
  })
  return (
    <figure
      aria-label={`${t('Financial summary')} · ${t('Trend')}`}
      className='space-y-2 px-3 pb-3 sm:px-4'
    >
      <figcaption className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
        {series.map((key) => (
          <span key={key} className='inline-flex items-center gap-1.5'>
            <span
              aria-hidden='true'
              className='size-2 rounded-sm'
              style={{ backgroundColor: config[key].color }}
            />
            {config[key].label}
          </span>
        ))}
      </figcaption>
      <ChartContainer
        className='aspect-auto h-28 w-full sm:h-32'
        config={config}
      >
        <LineChart
          data={props.points}
          accessibilityLayer
          margin={{ top: 4, left: 0, right: 5, bottom: 0 }}
        >
          <CartesianGrid vertical={false} />
          <XAxis
            dataKey='time'
            axisLine={false}
            tickLine={false}
            minTickGap={24}
            tickFormatter={(v: number) => axis.format(v * 1000)}
          />
          <YAxis
            domain={['auto', 'auto']}
            width='auto'
            tickCount={3}
            axisLine={false}
            tickLine={false}
            tickFormatter={formatQuota}
          />
          <ChartTooltip
            filterNull={false}
            content={
              <ChartTooltipContent
                labelFormatter={(_, payload) => {
                  const time = payload[0]?.payload?.time
                  return typeof time === 'number'
                    ? date.format(time * 1000)
                    : undefined
                }}
                formatter={(value, name) => (
                  <>
                    <span>{name}</span>
                    <strong className='ml-auto pl-3 tabular-nums'>
                      {value == null
                        ? t('Not recorded')
                        : formatLogQuota(Number(value))}
                    </strong>
                  </>
                )}
              />
            }
          />
          {series.map((key) => {
            const hasIsolatedPoint = props.points.some(
              (point, i) =>
                point[key] != null &&
                props.points[i - 1]?.[key] == null &&
                props.points[i + 1]?.[key] == null
            )
            return (
              <Line
                key={key}
                type='linear'
                dataKey={key}
                name={config[key].label}
                stroke={config[key].color}
                strokeWidth={2}
                strokeDasharray={key === 'cost' ? '4 3' : undefined}
                dot={
                  hasIsolatedPoint
                    ? { r: 2, fill: config[key].color, strokeWidth: 0 }
                    : false
                }
                isAnimationActive={false}
              />
            )
          })}
        </LineChart>
      </ChartContainer>
    </figure>
  )
}
