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
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { toIntlLocale } from '@/i18n/languages'
import {
  formatCompactNumber,
  formatNumber,
  formatQuota,
  formatTimeStr,
} from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  usePersonalUsage,
  type PersonalUsageMetric,
  type PersonalUsageRange,
} from '../../hooks/use-personal-usage'
import { ModelRequestShare } from './model-request-share'

const metrics = ['requests', 'tokens', 'usage'] as const

export function UsageTrend() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [range, setRange] = useState<PersonalUsageRange>('24h')
  const [metric, setMetric] = useState<PersonalUsageMetric>('requests')
  const query = usePersonalUsage(range)
  const labels = {
    requests: t('Requests'),
    tokens: t('Tokens'),
    usage: t('Usage'),
  }
  const axisDate = new Intl.DateTimeFormat(
    locale,
    range === '24h'
      ? { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }
      : { month: 'short', day: 'numeric' }
  )
  const tooltipDate = new Intl.DateTimeFormat(
    locale,
    range === '24h'
      ? {
          month: 'short',
          day: 'numeric',
          hour: '2-digit',
          minute: '2-digit',
          hourCycle: 'h23',
          timeZoneName: 'short',
        }
      : { year: 'numeric', month: 'short', day: 'numeric' }
  )
  const data = query.data

  return (
    <section className='min-w-0 space-y-3' aria-label={t('My usage trends')}>
      <div className='flex flex-wrap items-center justify-between gap-x-4 gap-y-2'>
        <h2 className='text-base font-semibold tracking-tight'>
          {t('My usage trends')}
        </h2>
        <div className='flex flex-wrap items-center gap-2'>
          <ToggleGroup
            size='sm'
            variant='outline'
            value={[range]}
            aria-label={t('Time range')}
            onValueChange={([value]) => {
              if (value === '24h' || value === '7d') setRange(value)
            }}
          >
            <ToggleGroupItem value='24h'>{t('Last 24 hours')}</ToggleGroupItem>
            <ToggleGroupItem value='7d'>{t('Last 7 days')}</ToggleGroupItem>
          </ToggleGroup>
          {!!query.dataUpdatedAt && (
            <span className='text-muted-foreground text-xs tabular-nums'>
              {t('Last updated:')}{' '}
              <time dateTime={new Date(query.dataUpdatedAt).toISOString()}>
                {formatTimeStr(new Date(query.dataUpdatedAt))}
              </time>
            </span>
          )}
          <Button
            type='button'
            size='icon-sm'
            variant='ghost'
            aria-label={t('Refresh')}
            title={t('Refresh')}
            disabled={query.isFetching || !query.isEnabled}
            onClick={() => void query.refetch()}
          >
            <RefreshCw
              aria-hidden='true'
              className={cn(query.isFetching && 'motion-safe:animate-spin')}
            />
          </Button>
        </div>
      </div>
      {query.isPending && (
        <LoadingState className='bg-card min-h-64 rounded-xl border' />
      )}
      {query.isError && (
        <ErrorState
          className='bg-card min-h-64 rounded-xl border'
          onRetry={() => void query.refetch()}
        />
      )}
      {query.isSuccess && data && data.rows.length === 0 && (
        <EmptyState className='bg-card min-h-64 rounded-xl' bordered />
      )}
      {query.isSuccess && data && data.rows.length > 0 && (
        <div className='grid items-stretch gap-3 xl:grid-cols-[minmax(0,2fr)_minmax(16rem,1fr)]'>
          <div className='bg-card min-w-0 rounded-xl border p-3 sm:p-5'>
            <ToggleGroup
              value={[metric]}
              spacing={1}
              aria-label={t('Value metric')}
              onValueChange={([value]) => {
                if (
                  value === 'requests' ||
                  value === 'tokens' ||
                  value === 'usage'
                ) {
                  setMetric(value)
                }
              }}
              className='mb-3 grid w-full grid-cols-3 gap-1.5'
            >
              {metrics.map((key) => {
                const value =
                  key === 'usage'
                    ? formatQuota(data.totals[key])
                    : formatNumber(data.totals[key], locale)
                return (
                  <ToggleGroupItem
                    key={key}
                    value={key}
                    aria-label={`${labels[key]} ${value}`}
                    className='aria-pressed:border-primary/30 aria-pressed:bg-primary/5 h-auto min-w-0 flex-col items-start gap-1 rounded-lg border border-transparent px-2 py-2 sm:px-3'
                  >
                    <span className='text-muted-foreground text-xs'>
                      {labels[key]}
                    </span>
                    <span
                      className='max-w-full truncate text-base font-semibold tracking-tight tabular-nums sm:text-xl'
                      title={value}
                    >
                      {value}
                    </span>
                  </ToggleGroupItem>
                )
              })}
            </ToggleGroup>

            <ChartContainer
              config={{
                [metric]: { label: labels[metric], color: 'var(--chart-1)' },
              }}
              className='aspect-auto h-52 w-full'
            >
              <AreaChart
                data={data.points}
                accessibilityLayer
                title={labels[metric]}
                margin={{ top: 10, bottom: 0, left: 0, right: 6 }}
              >
                <CartesianGrid vertical={false} strokeDasharray='3 4' />
                <XAxis
                  dataKey='time'
                  tickLine={false}
                  axisLine={false}
                  minTickGap={20}
                  tickMargin={8}
                  tickFormatter={(value: number) =>
                    axisDate.format(value * 1000)
                  }
                />
                <YAxis
                  tickLine={false}
                  axisLine={false}
                  width='auto'
                  tickMargin={8}
                  allowDecimals={false}
                  tickFormatter={(value: number) =>
                    metric === 'usage'
                      ? formatQuota(value)
                      : formatCompactNumber(value, locale)
                  }
                />
                <ChartTooltip
                  content={
                    <ChartTooltipContent
                      labelFormatter={(_, payload) =>
                        tooltipDate.format(
                          Number(payload?.[0]?.payload?.time) * 1000
                        )
                      }
                      formatter={(value) => (
                        <>
                          {labels[metric]}
                          <span className='ml-auto pl-2 font-medium tabular-nums'>
                            {metric === 'usage'
                              ? formatQuota(Number(value))
                              : formatNumber(Number(value), locale)}
                          </span>
                        </>
                      )}
                    />
                  }
                />
                <Area
                  type='linear'
                  dataKey={metric}
                  stroke={`var(--color-${metric})`}
                  fill={`var(--color-${metric})`}
                  fillOpacity={0.12}
                  strokeWidth={2}
                  isAnimationActive={false}
                />
              </AreaChart>
            </ChartContainer>
          </div>
          <ModelRequestShare data={data.rows} />
        </div>
      )}
    </section>
  )
}
