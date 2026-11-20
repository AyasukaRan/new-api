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

import { Progress } from '@/components/ui/progress'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { PanelWrapper } from '../ui/panel-wrapper'

export function ModelRequestShare(props: { data: readonly QuotaDataItem[] }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const percent = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const { rows, total } = useMemo(() => {
    const counts = new Map<string, number>()
    let total = 0
    for (const item of props.data) {
      const count = item.count ?? 0
      if (count <= 0) continue
      const model = item.model_name ?? ''
      counts.set(model, (counts.get(model) ?? 0) + count)
      total += count
    }
    const ranking = Array.from(counts, ([model, count]) => ({
      model,
      count,
    })).sort((a, b) => b.count - a.count || a.model.localeCompare(b.model))
    const rows: { model: string | null; count: number }[] = ranking.slice(0, 5)
    if (ranking.length > 5) {
      rows.push({
        model: null,
        count: total - rows.reduce((sum, row) => sum + row.count, 0),
      })
    }
    return { rows, total }
  }, [props.data])

  return (
    <PanelWrapper
      title={t('Top models')}
      description={t('Request share')}
      className='min-w-0'
      empty={rows.length === 0}
    >
      <ol className='grid gap-4' aria-label={t('Top models')}>
        {rows.map((row) => {
          const label =
            row.model === null ? t('Other models') : row.model || t('Unknown')
          const share = row.count / total
          return (
            <li
              key={row.model === null ? 'rest' : `model-${row.model}`}
              className='grid gap-1.5'
            >
              <div className='flex items-baseline justify-between gap-3 text-xs'>
                <span className='truncate font-medium' title={label}>
                  {label}
                </span>
                <span className='shrink-0 tabular-nums'>
                  <span className='text-muted-foreground mr-2'>
                    {formatNumber(row.count, locale)}
                  </span>
                  {percent.format(share)}
                </span>
              </div>
              <Progress
                aria-label={label}
                aria-valuetext={percent.format(share)}
                value={share * 100}
                className={cn(
                  '[&_[data-slot=progress-indicator]]:bg-chart-1',
                  row.model === null &&
                    '[&_[data-slot=progress-indicator]]:bg-muted-foreground'
                )}
              />
            </li>
          )
        })}
      </ol>
    </PanelWrapper>
  )
}
