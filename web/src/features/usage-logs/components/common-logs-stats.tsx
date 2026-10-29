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
import { getRouteApi } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { formatLogQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { getLogStats, getUserLogStats } from '../api'
import { buildApiParams } from '../lib/utils'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

function StatBadge(props: {
  label: string
  value: string | number
  accent: string
}) {
  return (
    <span
      role='group'
      aria-label={props.label}
      className='border-border/60 bg-muted/25 inline-flex h-7 items-center gap-2 rounded-md border px-2.5 text-xs shadow-xs'
    >
      <span className={cn('h-3.5 w-0.5 rounded-full', props.accent)} />
      <span className='text-muted-foreground'>{props.label}</span>
      <span className='text-foreground/85 font-mono font-semibold tabular-nums'>
        {props.value}
      </span>
    </span>
  )
}

export function CommonLogsStats() {
  const { t } = useTranslation()
  const { isAdminView: isAdmin, logSource } = useLogsViewScope()
  const user = useAuthStore((state) => state.auth.user)
  const searchParams = route.useSearch()
  const { sensitiveVisible } = useUsageLogsContext()
  const showFinance = isAdmin && logSource === 'usage'
  const params = useMemo(() => {
    const filters = buildApiParams({
      source: logSource,
      page: 1,
      pageSize: 1,
      searchParams,
      columnFilters: [],
      isAdmin,
    })
    delete filters.p
    delete filters.page_size
    return filters
  }, [isAdmin, logSource, searchParams])

  const {
    data: stats,
    isLoading,
    isError,
  } = useQuery({
    queryKey: [
      'usage-logs-stats',
      user?.id,
      user?.role,
      isAdmin,
      logSource,
      params,
    ],
    queryFn: async () => {
      const result = isAdmin
        ? requireServerSuccess(await getLogStats(params))
        : requireServerSuccess(await getUserLogStats(params))

      return result.data ?? null
    },
  })

  if (isLoading) {
    return (
      <div
        role='status'
        aria-label={t('Loading')}
        className='flex flex-wrap items-center gap-2'
      >
        <Skeleton className='h-7 w-[150px] rounded-md' />
        <Skeleton className='h-7 w-[100px] rounded-md' />
        <Skeleton className='h-7 w-[120px] rounded-md' />
        {showFinance && (
          <>
            <Skeleton className='h-7 w-[120px] rounded-md' />
            <Skeleton className='h-7 w-[120px] rounded-md' />
          </>
        )}
      </div>
    )
  }

  const currentStats = isError ? null : stats
  const finance = currentStats?.finance
  let costValue = t('Not recorded')
  let profitValue = t('Not recorded')
  if (isError) {
    costValue = t('Loading failed')
    profitValue = t('Loading failed')
  } else if (
    finance &&
    (finance.recorded_count > 0 ||
      (finance.recorded_count === 0 && finance.unrecorded_count === 0))
  ) {
    if (Number.isFinite(finance.cost_quota)) {
      costValue = formatLogQuota(finance.cost_quota)
    }
    if (Number.isFinite(finance.profit_quota)) {
      profitValue = formatLogQuota(finance.profit_quota)
    }
  }
  let usageValue = '—'
  if (currentStats && Number.isFinite(currentStats.quota)) {
    usageValue = formatLogQuota(currentStats.quota)
  }

  return (
    <div className='flex flex-wrap items-center gap-2'>
      <StatBadge
        label={logSource === 'test' ? t('Estimated test cost') : t('Usage')}
        value={sensitiveVisible ? usageValue : '••••'}
        accent='bg-sky-500/70'
      />
      <StatBadge
        label={t('RPM')}
        value={currentStats?.rpm ?? '—'}
        accent='bg-rose-500/65'
      />
      <StatBadge
        label={t('TPM')}
        value={currentStats?.tpm ?? '—'}
        accent='bg-slate-400/70'
      />
      {showFinance && (
        <>
          <StatBadge
            label={t('Upstream cost')}
            value={sensitiveVisible ? costValue : '••••'}
            accent='bg-amber-500/70'
          />
          <StatBadge
            label={t('Gross profit')}
            value={sensitiveVisible ? profitValue : '••••'}
            accent='bg-emerald-500/70'
          />
          {sensitiveVisible && finance && finance.unrecorded_count > 0 && (
            <p className='text-muted-foreground w-full text-xs'>
              {t('Cost and profit include only requests with recorded costs.')}
            </p>
          )}
        </>
      )}
    </div>
  )
}
