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

import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { LogOtherData } from '../types'

export function CacheHitRate(props: {
  promptTokens: number
  other: LogOtherData | null
}) {
  const { t, i18n } = useTranslation()
  const other = props.other
  const cacheRead = other?.cache_tokens
  if (cacheRead == null || !Number.isFinite(cacheRead) || cacheRead < 0) {
    return null
  }

  const splitWrites =
    (other?.cache_creation_tokens_5m || 0) +
    (other?.cache_creation_tokens_1h || 0)
  const cacheWrite = Math.max(
    0,
    other?.cache_write_tokens || 0,
    other?.cache_creation_tokens || 0,
    splitWrites
  )
  let inputTotal = other?.input_tokens_total
  if (inputTotal == null || !Number.isFinite(inputTotal) || inputTotal <= 0) {
    inputTotal = props.promptTokens
    // Claude and older split-cache logs store uncached input separately.
    if (
      other?.usage_semantic === 'anthropic' ||
      (!other?.usage_semantic && (other?.claude || splitWrites > 0))
    ) {
      inputTotal += cacheRead + cacheWrite
    }
  }
  if (
    !Number.isFinite(inputTotal) ||
    inputTotal <= 0 ||
    cacheRead > inputTotal ||
    !Number.isFinite(props.promptTokens) ||
    props.promptTokens < 0
  ) {
    return null
  }

  const percent = (cacheRead / inputTotal) * 100
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const label = t('Cache hit rate')
  const formatted = `${formatNumber(Math.round(percent * 10) / 10, locale)}%`
  let rateColor = 'text-emerald-700 dark:text-emerald-400'
  if (percent < 50) rateColor = 'text-orange-700 dark:text-orange-400'
  else if (percent < 80) rateColor = 'text-amber-700 dark:text-amber-400'

  return (
    <div className='mt-1 w-32 max-w-full space-y-1'>
      <div className='text-muted-foreground flex items-baseline justify-between gap-2 text-[11px]'>
        <span>{label}</span>
        <span className={cn('font-mono tabular-nums', rateColor)}>
          {formatted}
        </span>
      </div>
      <Progress
        value={percent}
        aria-label={label}
        aria-valuetext={formatted}
        className={cn(
          'w-full [&_[data-slot=progress-indicator]]:bg-current [&_[data-slot=progress-track]]:h-1.5 [&_[data-slot=progress-track]]:bg-muted-foreground/20 dark:[&_[data-slot=progress-track]]:bg-white/15',
          rateColor
        )}
      />
    </div>
  )
}
