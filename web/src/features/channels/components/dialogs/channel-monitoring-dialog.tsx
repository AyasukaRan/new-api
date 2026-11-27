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
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import { getChannelMonitoring } from '../../api'
import {
  getChannelDisplayBalance,
  isMultiKeyChannel,
  parseChannelSettings,
} from '../../lib'
import { formatChannelBalanceAmount } from '../../lib/channel-balance'
import { getKeyBalanceHistory } from '../../lib/channel-key-monitoring'
import type { ChannelMonitoring } from '../../types'
import {
  ChannelBalanceSummary,
  ChannelKeyBalanceValue,
} from '../channel-balance-summary'
import { ChannelKeyBalanceHistory } from '../channel-key-balance-history'
import { ChannelKeyUsageMonitoring } from '../channel-key-usage-monitoring'
import { useChannels } from '../channels-provider'

export function ChannelMonitoringContent(props: {
  data: ChannelMonitoring
  balanceQueryDisabled?: boolean
  multiKey?: boolean
  balanceRechargeMultiplier?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const data = props.data
  const multiplier =
    data.balance_recharge_multiplier ?? props.balanceRechargeMultiplier
  const multiKey =
    props.multiKey ||
    (data.balance?.key_accounts?.length ??
      data.balance?.key_balances.length ??
      0) > 1
  const multiKeyHistory =
    multiKey ||
    data.balance_history.some((sample) => sample.key_balances.length > 1)
  const usage = data.usage
  const stats = [
    {
      label: t('Request attempts'),
      value: formatNumber(usage.request_count, locale),
    },
    {
      label: t('Request success rate'),
      value:
        usage.success_rate == null
          ? t('No Data')
          : `${usage.success_rate.toFixed(1)}%`,
    },
    {
      label: t('Input / output tokens'),
      value: `${formatNumber(usage.recorded_input_tokens, locale)} / ${formatNumber(usage.recorded_output_tokens, locale)}`,
    },
    {
      label: t('Usage in this period'),
      value: formatQuotaWithCurrency(usage.recorded_used_quota),
    },
    {
      label: t('Cumulative usage'),
      value: formatQuotaWithCurrency(data.used_quota),
    },
    {
      label: t('Availability'),
      value:
        usage.availability_rate == null
          ? t('Not monitored')
          : `${usage.availability_rate.toFixed(1)}%`,
    },
  ]
  const chartData = getKeyBalanceHistory(
    data.balance_history,
    null,
    multiplier
  ).map((point) => ({
    time: formatTimestampToDate(point.timestamp),
    balance: point.balance,
  }))
  return (
    <div className='space-y-5'>
      <div className='grid grid-cols-2 gap-3'>
        {stats.map((stat) => (
          <div key={stat.label} className='min-w-0 rounded-lg border p-3'>
            <p className='text-muted-foreground text-xs'>{stat.label}</p>
            <p className='mt-1 text-lg font-semibold break-words tabular-nums'>
              {stat.value}
            </p>
          </div>
        ))}
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Request attempts include retries. Usage comes from retained billing records, including refunds. Health checks are excluded.'
        )}
      </p>
      <ChannelBalanceSummary
        balanceRechargeMultiplier={multiplier}
        detail={data.balance_detail}
        storedCurrency={data.balance_currency}
        monitor={data.balance}
        history={data.balance_daily_history ?? data.balance_history}
        queryDisabled={props.balanceQueryDisabled}
      />
      <ChannelKeyUsageMonitoring
        data={data}
        balanceRechargeMultiplier={multiplier}
      />
      {data.balance_history.length === 0 ? (
        <EmptyState
          className='min-h-32'
          title={t('No balance history yet')}
          description={
            props.balanceQueryDisabled
              ? t(
                  'Balance queries are disabled. Previously recorded balances are retained.'
                )
              : t(
                  'Refresh a balance or enable scheduled balance monitoring to start recording.'
                )
          }
        />
      ) : (
        <section className='space-y-3' aria-label={t('Balance history')}>
          <h3 className='text-sm font-medium'>{t('Balance history')}</h3>
          {multiKeyHistory ? (
            <>
              <p className='text-muted-foreground text-xs'>
                {t('Key numbers reflect the configuration at query time.')}
              </p>
              <ChannelKeyBalanceHistory
                history={data.balance_history}
                balanceRechargeMultiplier={multiplier}
                currency={data.balance_currency}
              />
            </>
          ) : (
            <ChartContainer
              className='h-44 w-full'
              config={{
                balance: {
                  label: t('Balance'),
                  color: 'var(--chart-1)',
                },
              }}
            >
              <LineChart accessibilityLayer data={chartData}>
                <CartesianGrid vertical={false} />
                <XAxis dataKey='time' hide />
                <YAxis
                  width={70}
                  tickFormatter={(value: number) =>
                    formatChannelBalanceAmount(value, data.balance_currency, {
                      locale,
                      compact: true,
                    })
                  }
                />
                <ChartTooltip
                  content={
                    <ChartTooltipContent
                      formatter={(value) =>
                        formatChannelBalanceAmount(
                          Number(value),
                          data.balance_currency,
                          { locale }
                        )
                      }
                    />
                  }
                />
                <Line
                  type='linear'
                  dataKey='balance'
                  stroke='var(--color-balance)'
                  strokeWidth={2}
                  dot={{ r: 3 }}
                  connectNulls={false}
                  isAnimationActive={false}
                />
              </LineChart>
            </ChartContainer>
          )}
          <StaticDataTable
            data={[...data.balance_history].reverse()}
            getRowKey={(sample) => sample.id}
            columns={[
              {
                id: 'time',
                header: t('Time'),
                cell: (sample) => formatTimestampToDate(sample.checked_at),
              },
              {
                id: 'balance',
                header: multiKeyHistory
                  ? t('Key balances')
                  : t('Queried balance'),
                cell: (sample) => {
                  if (!multiKeyHistory) {
                    if (!sample.success && !sample.partial) return '—'
                    const balance = getChannelDisplayBalance(
                      sample.known_balance,
                      multiplier
                    )
                    return balance == null
                      ? t('Not queried')
                      : formatChannelBalanceAmount(
                          balance,
                          data.balance_currency,
                          { locale }
                        )
                  }
                  if (!sample.key_balances.length) return t('Not queried')
                  return (
                    <div className='space-y-1'>
                      {sample.key_balances.map((row) => (
                        <div
                          key={row.index}
                          className='flex flex-wrap items-center gap-2'
                        >
                          <span className='text-muted-foreground text-xs'>
                            {t('Key #{{index}}', { index: row.index + 1 })}
                          </span>
                          <ChannelKeyBalanceValue
                            row={row}
                            currency={data.balance_currency}
                            balanceRechargeMultiplier={multiplier}
                          />
                        </div>
                      ))}
                    </div>
                  )
                },
              },
              {
                id: 'usage',
                header: t('Cumulative usage'),
                cell: (sample) => formatQuotaWithCurrency(sample.used_quota),
              },
              {
                id: 'status',
                header: t('Status'),
                cell: (sample) => {
                  if (sample.partial) return t('Partial result')
                  return sample.success ? t('Success') : t('Query failed')
                },
              },
            ]}
          />
        </section>
      )}
    </div>
  )
}

export function ChannelMonitoringDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const { currentRow, sensitiveVisible } = useChannels()
  const [hours, setHours] = useState(24)
  const query = useQuery({
    queryKey: ['channel-monitoring', currentRow?.id, hours],
    queryFn: () => {
      if (!currentRow) throw new Error('A channel is required')
      return getChannelMonitoring(currentRow.id, hours)
    },
    enabled: props.open && !!currentRow && sensitiveVisible,
    refetchInterval: props.open ? 60000 : false,
  })
  if (!currentRow) return null
  let content: ReactNode = <LoadingState />
  if (!sensitiveVisible) {
    content = <EmptyState title={t('Sensitive information is hidden')} />
  } else if (query.isError) {
    content = <ErrorState onRetry={() => void query.refetch()} />
  } else if (query.data) {
    content = (
      <ChannelMonitoringContent
        balanceRechargeMultiplier={
          parseChannelSettings(currentRow.setting)?.balance_recharge_multiplier
        }
        data={query.data}
        multiKey={isMultiKeyChannel(currentRow)}
        balanceQueryDisabled={
          parseChannelSettings(currentRow.setting)?.balance_query_disabled ===
          true
        }
      />
    )
  }
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Channel monitoring')}
      description={sensitiveVisible ? currentRow.name : '••••'}
      bodyClassName='space-y-4'
      contentClassName='sm:max-w-5xl'
      footer={
        <Button variant='outline' onClick={() => props.onOpenChange(false)}>
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-wrap items-center gap-2'>
        {[24, 168, 720].map((value) => (
          <Button
            key={value}
            variant={hours === value ? 'secondary' : 'outline'}
            size='sm'
            aria-pressed={hours === value}
            onClick={() => setHours(value)}
          >
            {
              {
                24: t('Last 24 hours'),
                168: t('Last 7 days'),
                720: t('Last 30 days'),
              }[value]
            }
          </Button>
        ))}
        <Button
          className='ml-auto'
          variant='outline'
          size='sm'
          disabled={query.isFetching || !sensitiveVisible}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </div>
      {content}
    </Dialog>
  )
}
