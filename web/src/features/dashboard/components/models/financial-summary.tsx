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
import { Banknote, TrendingUp, WalletCards } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Progress } from '@/components/ui/progress'
import { getUserQuotaDates } from '@/features/dashboard/api'
import {
  hourlyDashboardFinance,
  summarizeDashboardFinance,
} from '@/features/dashboard/lib/finance'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { StatCard } from '../ui/stat-card'
import { FinancialTrend } from './financial-trend'

interface FinancialSummaryProps {
  data: Array<Pick<QuotaDataItem, 'quota' | 'count' | 'finance'>>
  loading?: boolean
  error?: boolean
  title?: string
  scope?: string
  trend?: ReturnType<typeof hourlyDashboardFinance>
}

export function FinancialSummary(props: FinancialSummaryProps) {
  const { t, i18n } = useTranslation()
  const role = useAuthStore((state) => state.auth.user?.role)
  const summary = useMemo(
    () => summarizeDashboardFinance(props.data),
    [props.data]
  )
  if (!role || role < ROLE.ADMIN) return null
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const recorded = summary.recorded_count > 0
  const coverageCount = Math.max(
    summary.count,
    summary.recorded_count + summary.unrecorded_count
  )
  const coverage =
    coverageCount > 0 ? (summary.recorded_count / coverageCount) * 100 : 0
  const cards = [
    {
      title: t('Revenue'),
      value: formatQuota(summary.revenue_quota),
      description: t('Statistical quota'),
      icon: Banknote,
    },
    {
      title: t('Upstream cost'),
      value:
        recorded || coverageCount === 0
          ? formatQuota(summary.cost_quota)
          : t('Not recorded'),
      description: '',
      icon: WalletCards,
    },
    {
      title: t('Gross profit'),
      value:
        recorded || coverageCount === 0
          ? formatQuota(summary.profit_quota)
          : t('Not recorded'),
      description: `${t('Recorded revenue')}: ${formatQuota(summary.covered_revenue_quota)}`,
      icon: TrendingUp,
    },
  ]
  const percentage = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const margin =
    summary.covered_revenue_quota > 0 && recorded
      ? percentage.format(summary.profit_quota / summary.covered_revenue_quota)
      : '—'
  return (
    <section
      aria-label={props.title ?? t('Financial summary')}
      aria-busy={props.loading}
      className='dashboard-finance bg-card min-w-0 overflow-hidden rounded-xl border'
    >
      <div className='flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 px-4 pt-4 pb-3'>
        <h3 className='text-sm font-semibold'>
          {props.title ?? t('Financial summary')}
        </h3>
        {props.scope && (
          <span className='text-muted-foreground text-xs'>{props.scope}</span>
        )}
      </div>
      <div className='bg-muted/45 mx-4 grid grid-cols-1 divide-y rounded-md sm:grid-cols-3 sm:divide-x sm:divide-y-0'>
        {cards.map((card) => (
          <div
            key={card.title}
            role='group'
            aria-label={card.title}
            className='min-w-0 p-3 sm:p-4'
          >
            <StatCard
              {...card}
              loading={props.loading}
              error={props.error}
              compactMobile
            />
          </div>
        ))}
      </div>
      {!props.loading && !props.error && coverageCount > 0 && (
        <div className='space-y-2 px-4 py-3 text-xs'>
          <div className='flex flex-wrap items-baseline justify-between gap-2'>
            <span className='text-muted-foreground'>{t('Cost coverage')}</span>
            <span className='tabular-nums'>
              {formatNumber(summary.recorded_count, locale)} /{' '}
              {formatNumber(coverageCount, locale)} ·{' '}
              {percentage.format(coverage / 100)}
            </span>
          </div>
          <Progress aria-label={t('Cost coverage')} value={coverage} />
          <div className='flex flex-wrap items-baseline justify-between gap-2 pt-1'>
            <span className='text-muted-foreground'>
              {t('Gross margin on recorded revenue')}
            </span>
            <strong className='tabular-nums'>{margin}</strong>
          </div>
        </div>
      )}
      {!props.loading &&
        !props.error &&
        props.trend &&
        (coverageCount > 0 ? (
          <FinancialTrend points={props.trend} />
        ) : (
          <EmptyState title={t('No data available')} className='min-h-28' />
        ))}
      {!props.loading &&
        !props.error &&
        summary.recorded_count < coverageCount && (
          <p className='text-muted-foreground border-t px-3 py-2 text-xs sm:px-5'>
            {t('Cost and profit include only requests with recorded costs.')}
          </p>
        )}
    </section>
  )
}

export function OverviewFinancialSummary({ hours }: { hours: 24 | 168 }) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const isAdmin = Boolean(user && user.role >= ROLE.ADMIN)
  const query = useQuery({
    queryKey: ['dashboard', 'overview', 'finance', user?.id, user?.role, hours],
    queryFn: async () => {
      const end = Math.floor(Date.now() / 1000)
      const start = end - (end % 3600) - (hours - 1) * 3600
      const response = requireServerSuccess(
        await getUserQuotaDates(
          { start_timestamp: start, end_timestamp: end, default_time: 'hour' },
          true
        )
      )
      if (response.data !== null && !Array.isArray(response.data)) {
        throw new Error('Invalid response')
      }
      const rows = (response.data ?? []).filter(
        (row) => row.created_at >= start && row.created_at <= end
      )
      return { rows, trend: hourlyDashboardFinance(rows, start, end) }
    },
    enabled: isAdmin,
    staleTime: 60_000,
  })
  if (!isAdmin) return null
  if (query.isError) {
    return (
      <ErrorState
        title={t('Financial summary')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <FinancialSummary
      title={t('Financial summary')}
      scope={[
        t('All users'),
        hours === 24 ? t('Last 24 hours') : t('Last 7 days'),
      ].join(' · ')}
      data={query.data?.rows ?? []}
      trend={query.data?.trend}
      loading={query.isPending}
    />
  )
}
