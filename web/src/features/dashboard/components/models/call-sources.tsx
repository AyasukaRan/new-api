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
import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import { getSourceQuotaData } from '@/features/dashboard/api'
import { DEFAULT_TIME_GRANULARITY } from '@/features/dashboard/constants'
import { buildChartTimeDomain } from '@/features/dashboard/lib/charts'
import { getDefaultDays } from '@/features/dashboard/lib/filters'
import type {
  DashboardChartTimeDomain,
  DashboardFilters,
  ModelAnalyticsChartTab,
  QuotaDataItem,
  SourceQuotaDataItem,
} from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import {
  createServerError,
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { computeTimeRange } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { PanelWrapper } from '../ui/panel-wrapper'
import { ModelCharts } from './model-charts'

interface CallSourcesProps {
  filters?: DashboardFilters
  timeDomain?: DashboardChartTimeDomain
  activeTab?: ModelAnalyticsChartTab
  onActiveTabChange?: (tab: ModelAnalyticsChartTab) => void
}

export function CallSources(props: CallSourcesProps) {
  const { t, i18n } = useTranslation()
  // Currency formatters read the store directly; subscribe to update existing rows.
  useSystemConfigStore((state) => state.config.currency)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userId = useAuthStore((state) => state.auth.user?.id)
  const role = useAuthStore((state) => state.auth.user?.role)
  const isAdmin = Boolean(role && role >= ROLE.ADMIN)
  const timeGranularity =
    props.filters?.time_granularity ?? DEFAULT_TIME_GRANULARITY
  const params = useMemo(
    () => ({
      ...(props.timeDomain
        ? {
            start_timestamp: props.timeDomain.start_timestamp,
            end_timestamp: props.timeDomain.end_timestamp,
          }
        : computeTimeRange(
            getDefaultDays(props.filters?.time_granularity),
            props.filters?.start_timestamp,
            props.filters?.end_timestamp
          )),
      time_series: true,
      ...(isAdmin && props.filters?.username?.trim()
        ? { username: props.filters.username.trim() }
        : {}),
    }),
    [props.filters, props.timeDomain, isAdmin]
  )
  const timeDomain = useMemo(
    () => props.timeDomain ?? buildChartTimeDomain(params, timeGranularity),
    [props.timeDomain, timeGranularity, params]
  )
  const query = useQuery({
    queryKey: ['dashboard', 'sources', userId, role, params],
    queryFn: async () => {
      const response = requireServerSuccess(
        await getSourceQuotaData(params, isAdmin)
      )
      if (response.success !== true || !Array.isArray(response.data)) {
        throw createServerError(response, t('Failed to load call sources'))
      }
      return response.data
    },
    enabled: userId !== undefined,
    staleTime: 60_000,
  })
  const { rows, chartData, totalRequests } = useMemo(() => {
    const bySource = new Map<string, SourceQuotaDataItem>()
    const chartData: QuotaDataItem[] = []
    let totalRequests = 0
    for (const row of query.data ?? []) {
      const source = row.client_tool || ''
      const total = bySource.get(source) ?? {
        client_tool: source,
        count: 0,
        token_used: 0,
        quota: 0,
      }
      total.count += row.count
      total.token_used += row.token_used
      total.quota += row.quota
      bySource.set(source, total)
      totalRequests += row.count
      if (
        typeof row.created_at === 'number' &&
        Number.isFinite(row.created_at)
      ) {
        chartData.push({
          model_name: source || t('Unidentified source'),
          created_at: row.created_at,
          count: row.count,
          token_used: row.token_used,
          quota: row.quota,
        })
      }
    }
    const rows = [...bySource.values()].sort(
      (left, right) =>
        right.count - left.count ||
        left.client_tool.localeCompare(right.client_tool)
    )
    return { rows, chartData, totalRequests }
  }, [query.data, t])
  const percentFormatter = useMemo(
    () =>
      new Intl.NumberFormat(locale, {
        style: 'percent',
        maximumFractionDigits: 1,
      }),
    [locale]
  )

  let content
  if (query.isPending) {
    content = (
      <div role='status'>
        <LoadingState />
      </div>
    )
  } else if (query.isError) {
    content = (
      <ErrorState
        title={t('Failed to load call sources')}
        description={getServerErrorMessage(
          query.error,
          t('Please try again later.')
        )}
        onRetry={() => {
          void query.refetch()
        }}
        className='min-h-48'
      />
    )
  } else if (rows.length === 0) {
    content = <EmptyState title={t('No data available')} className='min-h-48' />
  } else {
    content = (
      <StaticDataTable
        data={rows}
        getRowKey={(row) => row.client_tool}
        className='max-h-[420px] overflow-auto'
        tableClassName='min-w-[640px]'
        tableProps={{ 'aria-label': t('Call Sources'), withContainer: false }}
        columns={[
          {
            id: 'source',
            header: t('Source'),
            cellClassName: 'max-w-64',
            cell: (row) => row.client_tool || t('Unidentified source'),
          },
          {
            id: 'requests',
            header: t('Requests'),
            className: 'text-right',
            cellClassName: 'text-right tabular-nums',
            cell: (row) => formatNumber(row.count, locale),
          },
          {
            id: 'share',
            header: t('Request share'),
            className: 'min-w-36',
            cell: (row) => {
              const share = totalRequests > 0 ? row.count / totalRequests : 0
              const source = row.client_tool || t('Unidentified source')
              return (
                <div className='space-y-1.5'>
                  <span className='text-muted-foreground tabular-nums'>
                    {percentFormatter.format(share)}
                  </span>
                  <Progress
                    value={share * 100}
                    aria-label={`${source} ${t('Request share')}`}
                  />
                </div>
              )
            },
          },
          {
            id: 'tokens',
            header: t('Tokens'),
            className: 'text-right',
            cellClassName: 'text-right tabular-nums',
            cell: (row) => formatNumber(row.token_used, locale),
          },
          {
            id: 'cost',
            header: t('Cost'),
            className: 'text-right',
            cellClassName: 'text-right tabular-nums',
            cell: (row) => formatQuotaWithCurrency(row.quota, { locale }),
          },
        ]}
      />
    )
  }

  const description = t(
    'Successful requests by client. Older records without source information appear as unidentified.'
  )
  const refreshButton = (
    <Button
      variant='ghost'
      size='sm'
      disabled={query.isFetching}
      onClick={() => {
        void query.refetch()
      }}
    >
      <RefreshCw className='size-3.5' aria-hidden='true' />
      {t('Refresh')}
    </Button>
  )

  return (
    <section aria-label={t('Call Sources')} aria-busy={query.isFetching}>
      {query.isSuccess && rows.length > 0 ? (
        <ModelCharts
          title={t('Call Sources')}
          description={description}
          headerActions={refreshButton}
          data={chartData}
          timeGranularity={timeGranularity}
          timeDomain={timeDomain}
          activeTab={props.activeTab}
          onActiveTabChange={props.onActiveTabChange}
        >
          {content}
        </ModelCharts>
      ) : (
        <PanelWrapper
          title={t('Call Sources')}
          description={description}
          className='rounded-lg'
          headerActions={refreshButton}
        >
          {content}
        </PanelWrapper>
      )}
    </section>
  )
}
