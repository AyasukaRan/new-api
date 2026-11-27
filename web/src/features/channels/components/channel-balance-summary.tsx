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

import { StaticDataTable } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatTimestampToDate } from '@/lib/format'

import {
  getBalanceVariant,
  getChannelDisplayBalance,
  getChannelKeyBalanceRows,
  getMultiKeyStatusConfig,
  type ChannelKeyBalanceRow,
} from '../lib'
import {
  formatChannelBalanceAmount,
  formatChannelBalanceSummary,
  getReportedChannelBalance,
} from '../lib/channel-balance'
import { getKeyDailyBalanceDecrease } from '../lib/channel-key-monitoring'
import type {
  ChannelBalanceDetail,
  ChannelBalanceMonitor,
  ChannelBalanceSample,
} from '../types'
import { ChannelBalanceDetails } from './channel-balance-details'

export function ChannelKeyBalanceValue({
  row,
  sensitiveVisible = true,
  balanceRechargeMultiplier,
  currency,
}: {
  row: ChannelKeyBalanceRow
  sensitiveVisible?: boolean
  currency?: string | null
  balanceRechargeMultiplier?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  if (!sensitiveVisible) return <span>••••</span>
  const balance = getChannelDisplayBalance(
    row.balance,
    balanceRechargeMultiplier
  )
  const lastKnownBalance = getChannelDisplayBalance(
    row.last_known_balance,
    balanceRechargeMultiplier
  )
  if (balance != null) {
    return (
      <StatusBadge
        label={formatChannelBalanceAmount(balance, currency, {
          locale,
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })}
        variant={getBalanceVariant(balance)}
        size='sm'
        copyable={false}
        showDot={false}
      />
    )
  }
  return (
    <div className='space-y-0.5'>
      <span className='text-muted-foreground text-xs'>
        {row.error ? t('Query failed') : t('Not queried')}
      </span>
      {lastKnownBalance != null && (
        <p className='text-muted-foreground text-[10px] leading-snug'>
          {t('Last known balance: {{balance}}', {
            balance: formatChannelBalanceAmount(lastKnownBalance, currency, {
              locale,
              digitsLarge: 2,
              digitsSmall: 4,
              abbreviate: false,
            }),
          })}
        </p>
      )}
    </div>
  )
}

function KeyBalanceGroups(props: { groups?: string[] }) {
  const { t } = useTranslation()
  if (!props.groups?.length) {
    return (
      <span className='text-muted-foreground text-xs'>
        {props.groups ? t('No groups') : t('Unknown group')}
      </span>
    )
  }
  return (
    <div className='flex min-w-0 flex-wrap gap-0.5'>
      {props.groups.map((group) => (
        <GroupBadge
          key={group}
          group={group}
          size='sm'
          showDot={false}
          copyable={false}
          className='max-w-full text-[10px]'
        />
      ))}
    </div>
  )
}

export function ChannelBalanceSummary(props: {
  history?: ChannelBalanceSample[]
  monitor: ChannelBalanceMonitor | null | undefined
  detail?: ChannelBalanceDetail | null
  storedCurrency?: string | null
  queryDisabled?: boolean
  balanceRechargeMultiplier?: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const monitor = props.monitor
  const reported = getReportedChannelBalance(
    props.detail,
    monitor,
    props.balanceRechargeMultiplier
  )
  const rows = getChannelKeyBalanceRows(monitor)
  const updatedAt = reported
    ? monitor?.checked_at
    : monitor?.balance_updated_time
  const formatBalance = (value: number | null | undefined) => {
    const balance = getChannelDisplayBalance(
      value,
      props.balanceRechargeMultiplier
    )
    return balance == null
      ? t('Not queried')
      : formatChannelBalanceAmount(balance, props.storedCurrency, {
          locale,
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })
  }
  let notice: string | null = null
  if (props.queryDisabled) {
    notice = t(
      'Balance queries are disabled. Previously recorded balances are retained.'
    )
  } else if (monitor?.configuration_changed) {
    notice = t(
      'Channel accounts changed. Refresh balances to see the current accounts.'
    )
  } else if (monitor?.partial) {
    notice = t(
      'Some accounts could not be queried. The total retains the last complete balance.'
    )
  } else if (
    monitor &&
    !monitor.success &&
    !reported &&
    monitor.checked_at > 0
  ) {
    notice = t(
      'The latest balance query failed. The last complete balance is retained.'
    )
  }

  return (
    <section className='space-y-3' aria-label={t('Balance monitoring')}>
      <div className='bg-muted/30 rounded-lg border p-4'>
        <p className='text-muted-foreground text-sm'>{t('Total balance')}</p>
        <p className='mt-1 text-2xl font-semibold tabular-nums'>
          {reported
            ? formatChannelBalanceSummary(reported, 0, {
                digitsLarge: 2,
                digitsSmall: 4,
                abbreviate: false,
                locale,
              })
            : formatBalance(
                monitor?.configuration_changed ? null : monitor?.balance
              )}
        </p>
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('Last updated:')}{' '}
          {updatedAt ? formatTimestampToDate(updatedAt) : t('Never')}
        </p>
        {!monitor?.configuration_changed && (
          <ChannelBalanceDetails result={props.detail} />
        )}
      </div>
      {notice && (
        <Alert>
          <AlertDescription>{notice}</AlertDescription>
        </Alert>
      )}
      {!!rows.length && (
        <StaticDataTable
          data={rows}
          getRowKey={(row) => row.index}
          columns={[
            {
              id: 'account',
              header: t('Account'),
              cell: (row) => t('Key #{{index}}', { index: row.index + 1 }),
            },
            {
              id: 'groups',
              header: t('Groups'),
              cell: (row) => <KeyBalanceGroups groups={row.groups} />,
            },
            {
              id: 'balance',
              header: t('Balance'),
              cell: (row) => {
                const keyDetail = props.detail?.keys?.[String(row.index)]
                const keyResult =
                  monitor?.configuration_changed ||
                  keyDetail?.error ||
                  (row.error && row.error !== 'non_numeric_balance')
                    ? undefined
                    : getReportedChannelBalance(
                        keyDetail,
                        undefined,
                        props.balanceRechargeMultiplier
                      )
                const dailyDecrease = monitor?.configuration_changed
                  ? null
                  : getKeyDailyBalanceDecrease(
                      props.history ?? [],
                      row.index,
                      props.balanceRechargeMultiplier
                    )
                return (
                  <div className='space-y-1'>
                    {keyResult ? (
                      <div>
                        {formatChannelBalanceSummary(keyResult, 0, { locale })}
                        <ChannelBalanceDetails result={keyDetail} />
                      </div>
                    ) : (
                      <ChannelKeyBalanceValue
                        row={row}
                        currency={props.storedCurrency}
                        balanceRechargeMultiplier={
                          props.balanceRechargeMultiplier
                        }
                      />
                    )}
                    {props.history && (
                      <TooltipProvider>
                        <Tooltip>
                          <TooltipTrigger
                            render={
                              <Button
                                type='button'
                                variant='link'
                                size='sm'
                                className='text-muted-foreground h-auto p-0 text-left text-xs font-normal whitespace-normal'
                              />
                            }
                          >
                            {t('Daily balance decrease: {{amount}}', {
                              amount:
                                dailyDecrease === null
                                  ? '—'
                                  : formatChannelBalanceAmount(
                                      dailyDecrease,
                                      props.storedCurrency,
                                      {
                                        locale,
                                        digitsSmall: 6,
                                        abbreviate: false,
                                      }
                                    ),
                            })}
                          </TooltipTrigger>
                          <TooltipContent>
                            {t(
                              'Positive declines between consecutive successful balance readings, divided by their observed days. Recharges, gaps and account changes are excluded; at least 24 hours are required. Shared balances do not measure individual key spending.'
                            )}
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    )}
                  </div>
                )
              },
            },
            {
              id: 'status',
              header: t('Status'),
              cell: (row) => {
                const status = getMultiKeyStatusConfig(row.status ?? 0)
                return (
                  <StatusBadge
                    label={t(status.label)}
                    variant={status.variant}
                    size='sm'
                    showDot={false}
                    copyable={false}
                  />
                )
              },
            },
          ]}
        />
      )}
    </section>
  )
}
