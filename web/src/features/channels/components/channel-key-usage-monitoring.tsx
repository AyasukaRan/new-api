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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toIntlLocale } from '@/i18n/languages'
import {
  formatCompactNumber,
  formatNumber,
  formatTimestampToDate,
} from '@/lib/format'

import {
  formatKeyUsageUSD,
  getKeyUsageTrend,
} from '../lib/channel-key-monitoring'
import type { ChannelMonitoring } from '../types'
import { ChannelKeyBalanceValue } from './channel-balance-summary'

export function ChannelKeyUsageMonitoring(props: {
  data: ChannelMonitoring
  balanceRechargeMultiplier?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const selectId = useId()
  const [selectedIdentity, setSelectedIdentity] = useState('')
  const [metric, setMetric] = useState<'tokens' | 'cost'>('tokens')
  const data = props.data.key_usage
  const keys = data?.keys ?? []
  const identity = keys.some((key) => key.key_identity === selectedIdentity)
    ? selectedIdentity
    : (keys[0]?.key_identity ?? '')
  const selected = keys.find((key) => key.key_identity === identity)
  const points = data ? getKeyUsageTrend(data, identity) : []
  const totalTokens = keys.reduce(
    (sum, key) => sum + key.input_tokens + key.output_tokens,
    0
  )
  const totalCost = keys.reduce((sum, key) => sum + (key.cost_usd ?? 0), 0)
  const observedStart = data?.observed_since
    ? Math.max(data.window_start, data.observed_since)
    : null
  const days =
    data && observedStart !== null
      ? (data.window_end - observedStart) / 86400
      : 0
  const percentage = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const options = keys.map((key) => ({
    value: key.key_identity,
    label:
      key.key_index < 0
        ? t('Removed key')
        : t('Key #{{index}}', { index: key.key_index + 1 }),
  }))
  const color = `var(--chart-${((((selected?.key_index ?? 0) % 5) + 5) % 5) + 1})`
  const hasUsage = keys.some(
    (key) => key.request_count > 0 || key.pending_requests > 0
  )
  return (
    <section className='space-y-4' aria-label={t('Key usage statistics')}>
      <h3 className='text-sm font-medium'>{t('Key usage statistics')}</h3>
      {data?.error && (
        <Alert>
          <AlertDescription>
            {t(
              'Key usage statistics are temporarily unavailable. Balance history is still available.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {!data?.error && !hasUsage && (
        <EmptyState
          className='min-h-28'
          title={t('No key usage recorded yet')}
          description={t(
            'Key usage is recorded after this feature is enabled. Earlier traffic is not reconstructed.'
          )}
        />
      )}
      {!!keys.length && !data?.error && (
        <>
          {hasUsage && (
            <>
              <div className='flex flex-wrap items-end justify-between gap-3'>
                <Field className='w-56 max-w-full'>
                  <FieldLabel htmlFor={selectId}>
                    {t('Usage history key')}
                  </FieldLabel>
                  <Select
                    items={options}
                    value={identity}
                    onValueChange={(value) => {
                      if (value !== null) setSelectedIdentity(value)
                    }}
                  >
                    <SelectTrigger id={selectId} className='w-full'>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {options.map((option) => (
                        <SelectItem key={option.value} value={option.value}>
                          {option.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <div
                  className='flex gap-2'
                  role='group'
                  aria-label={t('Usage metric')}
                >
                  <Button
                    size='sm'
                    variant={metric === 'tokens' ? 'secondary' : 'outline'}
                    aria-pressed={metric === 'tokens'}
                    onClick={() => setMetric('tokens')}
                  >
                    {t('Tokens')}
                  </Button>
                  <Button
                    size='sm'
                    variant={metric === 'cost' ? 'secondary' : 'outline'}
                    aria-pressed={metric === 'cost'}
                    onClick={() => setMetric('cost')}
                  >
                    {t('Upstream cost (USD)')}
                  </Button>
                </div>
              </div>
              {points.length === 0 ? (
                <EmptyState
                  className='min-h-28'
                  title={t('No key usage recorded yet')}
                />
              ) : (
                <ChartContainer
                  className='h-52 w-full'
                  config={{
                    tokens: { label: t('Tokens'), color },
                    cost: { label: t('Upstream cost (USD)'), color },
                  }}
                >
                  <LineChart accessibilityLayer data={points}>
                    <CartesianGrid vertical={false} />
                    <XAxis
                      dataKey='timestamp'
                      type='number'
                      domain={['dataMin', 'dataMax']}
                      tickFormatter={(value: number) =>
                        formatTimestampToDate(value)
                      }
                      minTickGap={60}
                      tickLine={false}
                    />
                    <YAxis
                      width={85}
                      tickFormatter={(value: number) =>
                        metric === 'tokens'
                          ? formatCompactNumber(value, locale)
                          : formatKeyUsageUSD(value, locale)
                      }
                    />
                    <ChartTooltip
                      content={
                        <ChartTooltipContent
                          labelFormatter={(_, payload) =>
                            formatTimestampToDate(
                              Number(payload[0]?.payload?.timestamp)
                            )
                          }
                          formatter={(value) =>
                            metric === 'tokens'
                              ? formatNumber(Number(value), locale)
                              : formatKeyUsageUSD(Number(value), locale)
                          }
                        />
                      }
                    />
                    <Line
                      type='monotone'
                      dataKey={metric}
                      stroke={`var(--color-${metric})`}
                      strokeWidth={2}
                      dot={{ r: 3 }}
                      connectNulls={false}
                      isAnimationActive={false}
                    />
                  </LineChart>
                </ChartContainer>
              )}
            </>
          )}
          <StaticDataTable
            data={keys}
            getRowKey={(key) => key.key_identity}
            tableProps={{ 'aria-label': t('Key usage statistics') }}
            columns={[
              {
                id: 'key',
                header: t('Key'),
                cell: (key) => (
                  <div className='space-y-1'>
                    <span className='font-medium'>
                      {key.key_index < 0
                        ? t('Removed key')
                        : t('Key #{{index}}', { index: key.key_index + 1 })}
                    </span>
                    <p
                      className='text-muted-foreground max-w-36 truncate font-mono text-xs'
                      title={key.key_preview}
                    >
                      {key.key_preview}
                    </p>
                  </div>
                ),
              },
              {
                id: 'weight',
                header: t('Weight / target share'),
                cell: (key) =>
                  `${formatNumber(key.weight, locale)} / ${percentage.format(key.target_share)}`,
              },
              {
                id: 'tokens',
                header: t('Tokens / actual share'),
                cell: (key) => (
                  <div>
                    {formatNumber(key.input_tokens + key.output_tokens, locale)}
                    <p className='text-muted-foreground text-xs'>
                      {totalTokens > 0
                        ? percentage.format(
                            (key.input_tokens + key.output_tokens) / totalTokens
                          )
                        : '—'}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t('Input / output tokens')}:{' '}
                      {formatNumber(key.input_tokens, locale)} /{' '}
                      {formatNumber(key.output_tokens, locale)}
                    </p>
                    {(key.tokens_estimated_requests ?? 0) > 0 && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Estimated token attempts: {{attempts}}', {
                          attempts: formatNumber(
                            key.tokens_estimated_requests,
                            locale
                          ),
                        })}
                      </p>
                    )}
                  </div>
                ),
              },
              {
                id: 'cost',
                header: t('USD cost / actual share'),
                cell: (key) => (
                  <div>
                    {key.cost_usd === null
                      ? t('Unknown')
                      : formatKeyUsageUSD(key.cost_usd, locale)}
                    <p className='text-muted-foreground text-xs'>
                      {key.cost_usd !== null && totalCost > 0
                        ? percentage.format(key.cost_usd / totalCost)
                        : '—'}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Actual: {{actual}} · Estimated: {{estimated}} · Unknown: {{unknown}}',
                        {
                          actual: formatNumber(
                            key.actual_cost_requests,
                            locale
                          ),
                          estimated: formatNumber(
                            key.estimated_cost_requests,
                            locale
                          ),
                          unknown: formatNumber(
                            key.unknown_cost_requests,
                            locale
                          ),
                        }
                      )}
                    </p>
                  </div>
                ),
              },
              {
                id: 'attempts',
                header: t('Request attempts'),
                cell: (key) => (
                  <div>
                    {formatNumber(key.request_count, locale)}
                    {key.pending_requests > 0 && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Pending: {{attempts}}', {
                          attempts: formatNumber(key.pending_requests, locale),
                        })}
                      </p>
                    )}
                  </div>
                ),
              },
              {
                id: 'daily',
                header: t('Daily average usage'),
                cell: (key) => {
                  if (days < 1) {
                    return (
                      <span className='text-muted-foreground'>
                        {t('Less than 1 day recorded')}
                      </span>
                    )
                  }
                  return (
                    <div>
                      {t('{{tokens}} tokens', {
                        tokens: formatNumber(
                          (key.input_tokens + key.output_tokens) / days,
                          locale
                        ),
                      })}
                      <p className='text-muted-foreground text-xs'>
                        {key.cost_usd === null
                          ? t('Unknown')
                          : formatKeyUsageUSD(key.cost_usd / days, locale)}
                      </p>
                    </div>
                  )
                },
              },
              {
                id: 'balance',
                header: t('Balance'),
                cell: (key) => {
                  const row =
                    !props.data.balance?.configuration_changed &&
                    key.key_index >= 0
                      ? props.data.balance?.key_balances.find(
                          (balance) => balance.index === key.key_index
                        )
                      : undefined
                  return row ? (
                    <ChannelKeyBalanceValue
                      row={row}
                      currency={props.data.balance_currency}
                      balanceRechargeMultiplier={
                        props.data.balance_recharge_multiplier ??
                        props.balanceRechargeMultiplier
                      }
                    />
                  ) : (
                    t('Not queried')
                  )
                },
              },
            ]}
          />
        </>
      )}
      {data && (
        <p className='text-muted-foreground text-xs'>
          {t('Usage window: {{start}} – {{end}}', {
            start: formatTimestampToDate(data.window_start),
            end: formatTimestampToDate(data.window_end),
          })}
        </p>
      )}
      {observedStart !== null && (
        <p className='text-muted-foreground text-xs'>
          {t('Recorded since {{time}}', {
            time: formatTimestampToDate(observedStart),
          })}
        </p>
      )}
      <p className='text-muted-foreground text-xs'>
        {t(
          'Cost shares use known costs only. Actual, estimated and unknown counts refer to request attempts. Daily averages use recorded days only. Shared balances are not individual key spending.'
        )}
      </p>
    </section>
  )
}
