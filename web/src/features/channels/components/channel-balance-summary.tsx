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
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatTimestampToDate } from '@/lib/format'

import {
  getBalanceVariant,
  getChannelDisplayBalance,
  getChannelKeyBalanceRows,
  getMultiKeyStatusConfig,
  type ChannelKeyBalanceRow,
} from '../lib'
import type { ChannelBalanceMonitor } from '../types'

export function ChannelKeyBalanceValue({
  row,
  sensitiveVisible = true,
  balanceRechargeMultiplier,
}: {
  row: ChannelKeyBalanceRow
  sensitiveVisible?: boolean
  balanceRechargeMultiplier?: number
}) {
  const { t } = useTranslation()
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
        label={formatCurrencyFromUSD(balance, {
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
            balance: formatCurrencyFromUSD(lastKnownBalance, {
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
  monitor: ChannelBalanceMonitor | null | undefined
  queryDisabled?: boolean
  balanceRechargeMultiplier?: number
}) {
  const { t } = useTranslation()
  const monitor = props.monitor
  const rows = getChannelKeyBalanceRows(monitor)
  const updatedAt = monitor?.balance_updated_time
  const formatBalance = (value: number | null | undefined) => {
    const balance = getChannelDisplayBalance(
      value,
      props.balanceRechargeMultiplier
    )
    return balance == null
      ? t('Not queried')
      : formatCurrencyFromUSD(balance, {
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
  } else if (monitor && !monitor.success && monitor.checked_at > 0) {
    notice = t(
      'The latest balance query failed. The last complete balance is retained.'
    )
  }

  return (
    <section className='space-y-3' aria-label={t('Balance monitoring')}>
      <div className='bg-muted/30 rounded-lg border p-4'>
        <p className='text-muted-foreground text-sm'>{t('Total balance')}</p>
        <p className='mt-1 text-2xl font-semibold tabular-nums'>
          {formatBalance(
            monitor?.configuration_changed ? null : monitor?.balance
          )}
        </p>
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('Last updated:')}{' '}
          {updatedAt ? formatTimestampToDate(updatedAt) : t('Never')}
        </p>
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
              cell: (row) => (
                <ChannelKeyBalanceValue
                  row={row}
                  balanceRechargeMultiplier={props.balanceRechargeMultiplier}
                />
              ),
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
