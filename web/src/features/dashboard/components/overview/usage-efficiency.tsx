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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatLogQuota, formatNumber } from '@/lib/format'
import type { TimeGranularity } from '@/lib/time'

import type { UsageSeries } from '../../hooks/use-personal-usage'
import { PanelWrapper } from '../ui/panel-wrapper'
import { UsageTimeChart } from './usage-time-chart'

export function UsageEfficiency(props: {
  data: UsageSeries
  granularity?: TimeGranularity
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [metric, setMetric] = useState<'unitCharge' | 'cache'>('cache')
  const { totals, points } = props.data
  const unavailable = t('Unavailable')
  const tokensPerRequest =
    totals.requests > 0
      ? formatNumber(totals.tokens / totals.requests, locale)
      : unavailable
  const unitCharge =
    totals.requests > 0
      ? formatLogQuota(totals.usage / totals.requests)
      : unavailable
  const metrics = [
    { label: t('Usage per request'), value: unitCharge },
    { label: t('Tokens per request'), value: tokensPerRequest },
    ...(
      [
        { key: 'cacheRead', label: t('Cache Read'), color: 'var(--chart-2)' },
        { key: 'cacheWrite', label: t('Cache Write'), color: 'var(--chart-4)' },
      ] as const
    ).map(({ key, label, color }) => {
      const value = totals[key]
      return {
        label,
        color,
        value: value == null ? unavailable : formatCompactNumber(value, locale),
        exact: value == null ? undefined : formatNumber(value, locale),
      }
    }),
  ]
  return (
    <PanelWrapper
      title={t('Cost efficiency')}
      className='min-w-0'
      contentClassName='space-y-4 p-3 sm:p-4'
      headerActions={
        <ToggleGroup
          size='sm'
          spacing={1}
          aria-label={t('Cost efficiency')}
          value={[metric]}
          className='bg-muted/70 p-0.5'
          onValueChange={([next]) => {
            if (next === 'unitCharge' || next === 'cache') setMetric(next)
          }}
        >
          <ToggleGroupItem
            className='aria-pressed:bg-background'
            value='unitCharge'
          >
            {t('Usage per request')}
          </ToggleGroupItem>
          <ToggleGroupItem className='aria-pressed:bg-background' value='cache'>
            {t('Cache')}
          </ToggleGroupItem>
        </ToggleGroup>
      }
    >
      <dl className='bg-muted/50 grid grid-cols-2 gap-2 rounded-lg px-3 py-2.5 sm:grid-cols-4'>
        {metrics.map((item) => (
          <div key={item.label} className='min-w-0'>
            <dt className='text-muted-foreground text-xs'>{item.label}</dt>
            <dd
              className='mt-1 flex items-center gap-1.5 text-sm font-semibold tabular-nums'
              title={'exact' in item ? item.exact : undefined}
            >
              {'color' in item && metric === 'cache' && (
                <span
                  aria-hidden='true'
                  className='size-2 shrink-0 rounded-sm'
                  style={{ backgroundColor: item.color }}
                />
              )}
              {item.value}
            </dd>
          </div>
        ))}
      </dl>
      <UsageTimeChart
        points={points}
        granularity={props.granularity ?? 'hour'}
        metric={metric}
      />
    </PanelWrapper>
  )
}
