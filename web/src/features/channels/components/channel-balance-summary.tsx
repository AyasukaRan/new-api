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
import { Alert, AlertDescription } from '@/components/ui/alert'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatTimestampToDate } from '@/lib/format'

import type { ChannelBalanceMonitor } from '../types'

export function ChannelBalanceSummary(props: {
  monitor: ChannelBalanceMonitor | null | undefined
  queryDisabled?: boolean
}) {
  const { t } = useTranslation()
  const monitor = props.monitor
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
    notice = t(
      'Some accounts could not be queried. The total retains the last complete balance.'
    )
  } else if (monitor && !monitor.success) {
    notice = t(
      'The latest balance query failed. The last complete balance is retained.'
    )
  }

  return (
    <section className='space-y-3' aria-label={t('Balance monitoring')}>
      <div className='bg-muted/30 rounded-lg border p-4'>
        <p className='text-muted-foreground text-sm'>{t('Total balance')}</p>
        <p className='mt-1 text-2xl font-semibold tabular-nums'>
          {formatBalance(monitor?.balance)}
        </p>
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('Last updated:')}{' '}
          {monitor?.balance_updated_time
            ? formatTimestampToDate(monitor.balance_updated_time)
            : t('Never')}
        </p>
      </div>
      {notice && (
        <Alert>
          <AlertDescription>{notice}</AlertDescription>
        </Alert>
      )}
      {monitor?.partial && (
        <p className='text-muted-foreground text-sm'>
          {t('Queried subtotal: {{balance}}', {
            balance: formatBalance(monitor.known_balance),
          })}
        </p>
      )}
      {!!monitor?.key_balances.length && (
        <StaticDataTable
          data={monitor.key_balances}
          getRowKey={(row) => row.index}
          columns={[
            {
              id: 'account',
              header: t('Account'),
              cell: (row) => t('Key #{{index}}', { index: row.index + 1 }),
            },
            {
              id: 'balance',
              header: t('Balance'),
              cell: (row) =>
                row.balance == null
                  ? t('Query failed')
                  : formatBalance(row.balance),
            },
          ]}
        />
      )}
    </section>
  )
}
