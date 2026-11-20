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
import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { formatLatency } from '@/features/performance-metrics/lib/format'
import type { GatewayDiagnostics } from '@/features/performance-metrics/types'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

type SortField = 'requests' | 'failed'

export function MonitoredModelHotspots(props: {
  models: GatewayDiagnostics['models']
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [sort, setSort] = useState<{ field: SortField; descending: boolean }>({
    field: 'requests',
    descending: true,
  })
  const rows = useMemo(() => {
    return props.models
      .filter((model) => model.request_count > 0)
      .map((model) => ({
        ...model,
        failed: model.request_count - model.success_count,
      }))
      .sort((a, b) => {
        const delta =
          sort.field === 'requests'
            ? a.request_count - b.request_count
            : a.failed - b.failed
        return (
          (sort.descending ? -delta : delta) ||
          b.request_count - a.request_count ||
          a.model_name.localeCompare(b.model_name)
        )
      })
  }, [props.models, sort])
  if (!rows.length) {
    return <EmptyState title={t('No data available')} className='min-h-44' />
  }

  return (
    <div className='max-h-61 min-h-44 overflow-auto rounded-lg border'>
      <Table
        withContainer={false}
        className='min-w-88 [&_td]:text-xs [&_th]:text-xs'
      >
        <caption className='sr-only'>{t('Model hotspots')}</caption>
        <TableHeader className='bg-muted sticky top-0 z-1'>
          <TableRow>
            <TableHead scope='col'>{t('Model')}</TableHead>
            {(['requests', 'failed'] as const).map((field) => {
              const active = sort.field === field
              let ariaSort: 'ascending' | 'descending' | 'none' = 'none'
              if (active) {
                ariaSort = sort.descending ? 'descending' : 'ascending'
              }
              let Icon = ArrowUpDown
              if (active) Icon = sort.descending ? ArrowDown : ArrowUp
              return (
                <TableHead
                  key={field}
                  scope='col'
                  aria-sort={ariaSort}
                  className='text-right'
                >
                  <Button
                    variant='ghost'
                    size='xs'
                    className='px-0.5'
                    onClick={() =>
                      setSort({
                        field,
                        descending: !active || !sort.descending,
                      })
                    }
                  >
                    {field === 'requests' ? t('Requests') : t('Failed')}{' '}
                    <Icon aria-hidden='true' />
                  </Button>
                </TableHead>
              )
            })}
            <TableHead scope='col' className='text-right'>
              {t('Average TTFT')}
            </TableHead>
            <TableHead scope='col' className='text-right'>
              {t('Average latency')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody className='[&_td]:py-1 [&>tr]:h-8'>
          {rows.map((model) => (
            <TableRow key={model.model_name}>
              <TableHead
                scope='row'
                className='h-auto max-w-48 truncate py-1 font-medium'
                title={model.model_name}
              >
                {model.model_name}
              </TableHead>
              <TableCell className='text-right'>
                {formatNumber(model.request_count, locale)}
              </TableCell>
              <TableCell
                className={
                  model.failed > 0
                    ? 'text-destructive text-right'
                    : 'text-muted-foreground text-right'
                }
              >
                {formatNumber(model.failed, locale)}
              </TableCell>
              <TableCell className='text-right'>
                {model.avg_ttft_ms === 0
                  ? '0ms'
                  : formatLatency(model.avg_ttft_ms ?? Number.NaN)}
              </TableCell>
              <TableCell className='text-right'>
                {formatLatency(model.avg_latency_ms)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
