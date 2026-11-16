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
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatTimestampToDate } from '@/lib/format'

import {
  getBalanceVariant,
  getChannelKeyBalanceRows,
  getMultiKeyStatusConfig,
  type ChannelKeyBalanceRow,
} from '../lib'
import type { ChannelBalanceMonitor } from '../types'

export function ChannelKeyBalanceValue({
  row,
  sensitiveVisible = true,
}: {
  row: ChannelKeyBalanceRow
  sensitiveVisible?: boolean
}) {
  const { t } = useTranslation()
  if (!sensitiveVisible) return <span>••••</span>
  if (row.balance != null) {
    return (
      <StatusBadge
        label={formatCurrencyFromUSD(row.balance, {
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })}
        variant={getBalanceVariant(row.balance)}
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
      {row.last_known_balance != null && (
        <p className='text-muted-foreground text-[10px] leading-snug'>
          {t('Last known balance: {{balance}}', {
            balance: formatCurrencyFromUSD(row.last_known_balance, {
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

function KeyBalanceGroups({
  groups,
  compact = false,
}: {
  groups?: string[]
  compact?: boolean
}) {
  const { t } = useTranslation()
  if (!groups?.length) {
    return (
      <span className='text-muted-foreground text-xs'>
        {groups ? t('No groups') : t('Unknown group')}
      </span>
    )
  }
  if (compact) {
    return (
      <GroupBadge
        group={groups[0]}
        label={groups.join(' · ')}
        size='sm'
        showDot={false}
        copyable={false}
        className='max-w-full text-[10px]'
      />
    )
  }
  return (
    <div className='flex min-w-0 flex-wrap gap-0.5'>
      {groups.map((group) => (
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

export function ChannelKeyBalanceList(props: {
  monitor: ChannelBalanceMonitor | null | undefined
  sensitiveVisible: boolean
  onViewAll: () => void
}) {
  const { t } = useTranslation()
  const rows = getChannelKeyBalanceRows(props.monitor)
  return (
    <div
      className='w-full min-w-0 space-y-1.5 pl-1.5'
      data-testid='multi-key-balances'
    >
      {rows.length === 0 && (
        <span className='text-muted-foreground text-xs'>
          {t('Not queried')}
        </span>
      )}
      {rows.slice(0, 3).map((row) => {
        const status = getMultiKeyStatusConfig(row.status ?? 1)
        return (
          <div
            key={row.index}
            className='border-border min-w-0 space-y-0.5 border-l-2 pl-2'
          >
            <div className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5'>
              <span
                className='text-muted-foreground text-[10px]'
                aria-label={t('Key #{{index}}', { index: row.index + 1 })}
              >
                #{row.index + 1}
              </span>
              <div className='min-w-0 text-sm font-medium tabular-nums'>
                <ChannelKeyBalanceValue
                  row={row}
                  sensitiveVisible={props.sensitiveVisible}
                />
              </div>
              {row.status != null && row.status !== 1 && (
                <StatusBadge
                  label={t(status.label)}
                  variant={status.variant}
                  size='sm'
                  showDot={false}
                  copyable={false}
                  className='text-[10px]'
                />
              )}
            </div>
            {props.sensitiveVisible ? (
              <KeyBalanceGroups groups={row.groups} compact />
            ) : (
              <span className='text-muted-foreground text-[10px]'>••••</span>
            )}
          </div>
        )
      })}
      {rows.length > 3 && (
        <Button
          variant='link'
          size='sm'
          className='h-auto p-0 text-xs'
          onClick={props.onViewAll}
        >
          {t('View all {{count}} keys', { count: rows.length })}
        </Button>
      )}
    </div>
  )
}

export function ChannelBalanceSummary(props: {
  monitor: ChannelBalanceMonitor | null | undefined
  queryDisabled?: boolean
  multiKey?: boolean
}) {
  const { t } = useTranslation()
  const monitor = props.monitor
  const rows = getChannelKeyBalanceRows(monitor)
  const multiKey = props.multiKey || rows.length > 1
  const updatedAt = multiKey
    ? monitor?.checked_at
    : monitor?.balance_updated_time
  const formatBalance = (value: number | null | undefined) =>
    value == null
      ? t('Not queried')
      : formatCurrencyFromUSD(value, {
          digitsLarge: 2,
          digitsSmall: 4,
          abbreviate: false,
        })
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
    notice = multiKey
      ? t(
          'Some key balances could not be queried. Check each key for its latest result.'
        )
      : t(
          'Some accounts could not be queried. The total retains the last complete balance.'
        )
  } else if (monitor && !monitor.success && monitor.checked_at > 0) {
    notice = multiKey
      ? t(
          'The latest balance query failed. Check each key for its last known balance.'
        )
      : t(
          'The latest balance query failed. The last complete balance is retained.'
        )
  }

  return (
    <section className='space-y-3' aria-label={t('Balance monitoring')}>
      <div className='bg-muted/30 rounded-lg border p-4'>
        <p className='text-muted-foreground text-sm'>
          {multiKey ? t('Key balances') : t('Total balance')}
        </p>
        {!multiKey && (
          <p className='mt-1 text-2xl font-semibold tabular-nums'>
            {formatBalance(
              monitor?.configuration_changed ? null : monitor?.balance
            )}
          </p>
        )}
        {multiKey && rows.length === 0 && (
          <p className='mt-1 text-sm'>{t('Not queried')}</p>
        )}
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
              cell: (row) => <ChannelKeyBalanceValue row={row} />,
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
