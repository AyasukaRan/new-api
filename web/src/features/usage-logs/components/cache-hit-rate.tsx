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

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getLogCacheUsage } from '../lib/cache-usage'
import type { LogOtherData } from '../types'

export function CacheHitRing(props: {
  percent: number | null
  inheritFont?: boolean
}) {
  const { t, i18n } = useTranslation()
  if (props.percent == null) {
    return <span className='text-muted-foreground'>—</span>
  }

  const percent = props.percent
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const formatted = `${formatNumber(Math.round(percent * 10) / 10, locale)}%`
  let color = 'text-emerald-700 dark:text-emerald-400'
  if (percent < 50) color = 'text-orange-700 dark:text-orange-400'
  else if (percent < 80) color = 'text-amber-700 dark:text-amber-400'

  return (
    <span
      role='progressbar'
      aria-label={t('Cache hit rate')}
      aria-valuenow={percent}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuetext={formatted}
      className={cn(
        'inline-flex items-center justify-end gap-1 align-middle',
        color
      )}
    >
      <svg
        aria-hidden='true'
        viewBox='0 0 16 16'
        className='size-3.5 shrink-0 -rotate-90'
        fill='none'
        strokeWidth='2.75'
      >
        <circle
          cx='8'
          cy='8'
          r='6'
          className='stroke-muted-foreground/40 dark:stroke-muted-foreground/50'
        />
        <circle
          cx='8'
          cy='8'
          r='6'
          stroke='currentColor'
          pathLength='100'
          strokeDasharray={`${percent} 100`}
        />
      </svg>
      <span
        className={cn(
          'min-w-[4ch] text-end tabular-nums',
          !props.inheritFont && 'font-mono'
        )}
      >
        {formatted}
      </span>
    </span>
  )
}

export function CacheHitRate(props: {
  promptTokens: number
  other: LogOtherData | null
}) {
  const { t } = useTranslation()
  const { percent } = getLogCacheUsage(props.promptTokens, props.other)
  if (percent == null) return null
  return (
    <div className='text-muted-foreground inline-flex items-center gap-2 text-xs'>
      {t('Cache hit rate')}
      <CacheHitRing percent={percent} />
    </div>
  )
}
