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
import { ArrowDown } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { PersonalUsageMetric } from '@/features/dashboard/hooks/use-personal-usage'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber, formatQuota } from '@/lib/format'

import { PanelWrapper } from '../ui/panel-wrapper'

type ModelUsage = Record<PersonalUsageMetric, number>

export function ModelRequestShare(props: {
  data: readonly QuotaDataItem[]
  metric?: PersonalUsageMetric
  onMetricChange?: (metric: PersonalUsageMetric) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const metric = props.metric ?? 'requests'
  const percent = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const { rows, total } = useMemo(() => {
    const models = new Map<string, ModelUsage>()
    for (const item of props.data) {
      const model = item.model_name ?? ''
      const usage = models.get(model) ?? { requests: 0, tokens: 0, usage: 0 }
      usage.requests += item.count ?? 0
      usage.tokens += item.token_used ?? 0
      usage.usage += item.quota ?? 0
      models.set(model, usage)
    }
    const ranked = [...models.entries()].sort(
      ([nameA, a], [nameB, b]) =>
        b[metric] - a[metric] || nameA.localeCompare(nameB)
    )
    const rows: [string | null, ModelUsage][] = ranked.slice(0, 5)
    if (ranked.length > 5) {
      const others = { requests: 0, tokens: 0, usage: 0 }
      for (const [, row] of ranked.slice(5)) {
        others.requests += row.requests
        others.tokens += row.tokens
        others.usage += row.usage
      }
      rows.push([null, others])
    }
    return { rows, total: rows.reduce((sum, [, row]) => sum + row[metric], 0) }
  }, [metric, props.data])

  const titles = {
    requests: t('Request share'),
    tokens: t('Token share'),
    usage: t('Usage share'),
  }
  const columns = [
    { key: 'requests', label: t('Requests') },
    { key: 'tokens', label: t('Tokens') },
    { key: 'usage', label: t('Billed usage') },
  ] as const
  return (
    <PanelWrapper
      title={t('Model hotspots')}
      description={titles[metric]}
      className='min-w-0'
      contentClassName='px-2 pb-2 pt-0 sm:px-3 sm:pb-3 sm:pt-0'
      empty={rows.length === 0}
    >
      <Table
        aria-label={t('Model hotspots')}
        className='table-fixed [&_td]:text-xs [&_th]:text-xs'
      >
        <TableHeader className='bg-muted/60'>
          <TableRow>
            <TableHead scope='col' className='w-[37%] rounded-l-md'>
              {t('Model')}
            </TableHead>
            {columns.map(({ key, label }) => (
              <TableHead
                key={key}
                scope='col'
                aria-sort={metric === key ? 'descending' : undefined}
                className='px-1 text-right whitespace-normal last:rounded-r-md'
              >
                {props.onMetricChange ? (
                  <Button
                    variant='ghost'
                    size='sm'
                    className='h-auto max-w-full justify-end gap-0.5 px-0.5 text-right text-xs whitespace-normal'
                    onClick={() => props.onMetricChange?.(key)}
                  >
                    {label}
                    {metric === key && (
                      <ArrowDown className='size-3' aria-hidden='true' />
                    )}
                  </Button>
                ) : (
                  label
                )}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody className='[&>tr:nth-child(even)]:bg-muted/30 [&>tr]:h-auto'>
          {rows.map(([model, values]) => {
            const label =
              model === null ? t('Other models') : model || t('Unknown')
            const share = total > 0 ? values[metric] / total : null
            return (
              <TableRow key={model === null ? 'other' : `model-${model}`}>
                <TableHead scope='row' className='h-auto py-2'>
                  <span className='block truncate' title={label}>
                    {label}
                  </span>
                  {share !== null && (
                    <div className='mt-1 flex items-center gap-2'>
                      <Progress
                        aria-label={label}
                        aria-valuetext={percent.format(share)}
                        value={share * 100}
                        className='[&_[data-slot=progress-indicator]]:bg-chart-1 min-w-4 flex-1'
                      />
                      <span
                        className='text-muted-foreground tabular-nums'
                        data-table-text='secondary'
                      >
                        {percent.format(share)}
                      </span>
                    </div>
                  )}
                </TableHead>
                {columns.map(({ key }) => {
                  const full =
                    key === 'usage'
                      ? formatQuota(values[key])
                      : formatNumber(values[key], locale)
                  return (
                    <TableCell
                      key={key}
                      className='px-1 text-right tabular-nums'
                      title={full}
                    >
                      {key === 'usage'
                        ? full
                        : formatCompactNumber(values[key], locale)}
                    </TableCell>
                  )
                })}
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </PanelWrapper>
  )
}
