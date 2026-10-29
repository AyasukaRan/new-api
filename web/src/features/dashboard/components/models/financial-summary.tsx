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

import { getUserQuotaDates } from '@/features/dashboard/api'
import { summarizeDashboardFinance } from '@/features/dashboard/lib/finance'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatQuota } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { getRollingDateRange } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'

import { StatCard } from '../ui/stat-card'

interface FinancialSummaryProps {
  data: Array<Pick<QuotaDataItem, 'quota' | 'count' | 'finance'>>
  loading?: boolean
  error?: boolean
  title?: string
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
      value: recorded ? formatQuota(summary.cost_quota) : t('Not recorded'),
      description:
        summary.unrecorded_count > 0
          ? `${t('Cost coverage')}: ${formatNumber(Math.round(coverage * 10) / 10, locale)}%`
          : '',
      icon: WalletCards,
    },
    {
      title: t('Gross profit'),
      value: recorded ? formatQuota(summary.profit_quota) : t('Not recorded'),
      description: `${t('Recorded revenue')}: ${formatQuota(summary.covered_revenue_quota)}`,
      icon: TrendingUp,
    },
  ]
  return (
    <section
      aria-label={props.title ?? t('Financial summary')}
      aria-busy={props.loading}
      className='overflow-hidden rounded-lg border'
    >
      <div className='border-b px-3 py-2 text-sm font-semibold sm:px-5'>
        {props.title ?? t('Financial summary')}
      </div>
      <div className='grid grid-cols-1 divide-y sm:grid-cols-3 sm:divide-x sm:divide-y-0'>
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
      {!props.loading && !props.error && summary.unrecorded_count > 0 && (
        <p className='text-muted-foreground border-t px-3 py-2 text-xs sm:px-5'>
          {t('Cost and profit include only requests with recorded costs.')}
        </p>
      )}
    </section>
  )
}

export function OverviewFinancialSummary() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const isAdmin = Boolean(user && user.role >= ROLE.ADMIN)
  const range = useMemo(() => {
    const { start, end } = getRollingDateRange(1)
    return {
      start_timestamp: Math.floor(start.getTime() / 1000),
      end_timestamp: Math.floor(end.getTime() / 1000),
    }
  }, [])
  const query = useQuery({
    queryKey: ['dashboard', 'overview', 'finance', user?.id, user?.role, range],
    queryFn: async () =>
      requireServerSuccess(
        await getUserQuotaDates({ ...range, default_time: 'hour' }, true)
      ).data,
    enabled: isAdmin,
    staleTime: 60_000,
  })
  if (!isAdmin) return null
  return (
    <FinancialSummary
      title={t('Last 24h financial summary')}
      data={query.data ?? []}
      loading={query.isPending}
      error={query.isError}
    />
  )
}
