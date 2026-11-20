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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { useModelStatCardsConfig } from '@/features/dashboard/hooks/use-dashboard-config'
import { calculateDashboardStats } from '@/features/dashboard/lib'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber, formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

interface LogStatCardsProps {
  data: QuotaDataItem[]
  loading: boolean
  error: boolean
  timeRangeMinutes: number
}

const MAX_INLINE_STAT_CHARS = 7

function formatStatNumber(value: number, locale: Intl.LocalesArgument) {
  const fullValue = formatNumber(value, locale)
  const displayValue =
    fullValue.length > MAX_INLINE_STAT_CHARS
      ? formatCompactNumber(value, locale)
      : fullValue

  return {
    displayValue,
    fullValue,
  }
}

export function LogStatCards(props: LogStatCardsProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const statCardsConfig = useModelStatCardsConfig()
  const { data, loading, error, timeRangeMinutes } = props
  const stats = useMemo(() => calculateDashboardStats(data), [data])

  const adaptedStats = {
    rpm: stats.totalCount,
    quota: stats.totalQuota,
    tpm: stats.totalTokens,
    cacheRead: stats.totalCacheReadTokens,
    cacheCreation: stats.totalCacheCreationTokens,
  }

  const items = statCardsConfig.map((config) => {
    const rawValue = config.getValue(adaptedStats, timeRangeMinutes)
    const formatted =
      config.key === 'quota'
        ? {
            displayValue: formatQuota(rawValue),
            fullValue: formatQuota(rawValue),
          }
        : formatStatNumber(rawValue, locale)

    return {
      key: config.key,
      title: config.title,
      value: formatted.displayValue,
      fullValue: formatted.fullValue,
      desc: config.description,
      icon: config.icon,
      iconTone: config.iconTone,
    }
  })

  return (
    <dl
      className='bg-card grid min-w-0 grid-cols-2 gap-px overflow-hidden rounded-xl border sm:grid-cols-4 xl:grid-cols-7'
      aria-label={t('Usage at a glance')}
      aria-busy={loading}
    >
      {items.map((item, index) => {
        const Icon = item.icon
        const missing =
          (item.key === 'cacheRead' && !stats.cacheReadRecorded) ||
          (item.key === 'cacheCreation' && !stats.cacheCreationRecorded)
        const value = error || missing ? t('Unavailable') : item.value
        return (
          <div
            key={item.key}
            className={cn(
              'bg-muted/40 min-w-0 px-3 py-2.5 sm:px-4 sm:py-3',
              index === items.length - 1 && 'col-span-2 sm:col-span-1'
            )}
          >
            <dt
              className='text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs'
              title={item.desc}
            >
              <IconBadge tone={item.iconTone} size='xs'>
                <Icon />
              </IconBadge>
              {item.title}
            </dt>
            <dd
              className='mt-1 truncate text-base font-semibold tabular-nums sm:text-lg'
              title={error || missing ? undefined : item.fullValue}
            >
              {loading ? <Skeleton className='h-6 w-16' /> : value}
            </dd>
          </div>
        )
      })}
    </dl>
  )
}
