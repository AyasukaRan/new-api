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

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota, formatTimeStr } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  usePersonalUsage,
  type PersonalUsageMetric,
  type PersonalUsageRange,
} from '../../hooks/use-personal-usage'
import { PanelWrapper } from '../ui/panel-wrapper'
import { ModelRequestShare } from './model-request-share'
import { UsageEfficiency } from './usage-efficiency'
import { UsageTimeChart } from './usage-time-chart'

const metrics = ['requests', 'tokens', 'usage'] as const

export function UsageTrend() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [range, setRange] = useState<PersonalUsageRange>('24h')
  const [metric, setMetric] = useState<PersonalUsageMetric>('tokens')
  const [rankBy, setRankBy] = useState<PersonalUsageMetric>('tokens')
  const query = usePersonalUsage(range)
  const labels = {
    requests: t('Requests'),
    tokens: t('Tokens'),
    usage: t('Billed usage'),
  }
  const data = query.data
  const granularity = range === '24h' ? 'hour' : 'day'
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
      {query.isSuccess && data?.rows.length === 0 && (
        <EmptyState className='bg-card min-h-64 rounded-xl' bordered />
      )}
      <div className='grid min-w-0 gap-3 lg:grid-cols-2'>
        {query.isSuccess && data && data.rows.length > 0 && (
          <>
            <PanelWrapper
              title={t('Traffic volume')}
              className='min-w-0'
              contentClassName='space-y-4 p-3 sm:p-4'
            >
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
                    setRankBy(value)
                  }
                }}
                className='bg-muted/50 grid w-full grid-cols-3 gap-1 rounded-lg p-1'
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
                      className='aria-pressed:bg-background h-auto min-w-0 flex-col items-start gap-1 rounded-md px-2 py-2 aria-pressed:shadow-xs sm:px-3'
                    >
                      <span className='text-muted-foreground text-xs'>
                        {labels[key]}
                      </span>
                      <span
                        className='max-w-full truncate text-sm font-semibold tabular-nums sm:text-base'
                        title={value}
                      >
                        {value}
                      </span>
                    </ToggleGroupItem>
                  )
                })}
              </ToggleGroup>
              <UsageTimeChart
                points={data.points}
                metric={metric}
                granularity={granularity}
              />
            </PanelWrapper>
            <ModelRequestShare
              data={data.rows}
              metric={rankBy}
              onMetricChange={setRankBy}
            />
            <div className='min-w-0 lg:col-span-2'>
              <UsageEfficiency data={data} granularity={granularity} />
            </div>
          </>
        )}
      </div>
    </section>
  )
}
