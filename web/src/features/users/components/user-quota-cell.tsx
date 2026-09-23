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

import { QuotaDetailsPopover } from '@/components/quota-details-popover'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { formatQuotaWithCurrency, getCurrencyDisplay } from '@/lib/currency'
import { formatQuota, formatTimestamp } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import type { UserSubscriptionSummary } from '../types'

type UserQuotaCellProps = {
  remaining: number
  subscriptions?: UserSubscriptionSummary[]
  onManageSubscriptions?: () => void
  used: number
}

export function UserQuotaCell(props: UserQuotaCellProps) {
  const { t } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)

  const hiddenSubscriptionCount = Math.max(
    0,
    (props.subscriptions?.length ?? 0) - 2
  )
  const { meta: currency } = getCurrencyDisplay()
  const quotaUnit = currency.kind === 'tokens' ? t('Tokens') : currency.symbol
  const hasQuota = props.remaining !== 0 || props.used !== 0
  const formattedRemaining = formatQuotaWithCurrency(props.remaining, {
    showSymbol: false,
  })
  const formattedUsed = formatQuotaWithCurrency(props.used, {
    showSymbol: false,
  })

  return (
    <div className='w-full min-w-0 space-y-3 py-1'>
      <QuotaDetailsPopover
        title={`${t('Quota')} (${quotaUnit})`}
        triggerLabel={
          hasQuota
            ? `${t('Available Balance')} ${formattedRemaining}; ${t('Used amount')} ${formattedUsed}`
            : t('No Quota')
        }
        details={[
          { label: t('Available Balance'), value: formattedRemaining },
          { label: t('Total Used'), value: formattedUsed },
        ]}
      >
        {hasQuota ? (
          <span className='grid min-w-0 grid-cols-1 gap-y-1 text-sm tabular-nums'>
            <span
              className={cn(
                props.remaining < 0 && 'text-destructive',
                props.remaining === 0 && 'text-muted-foreground'
              )}
            >
              {formattedRemaining}
            </span>
            <span
              data-table-text='secondary'
              className='text-muted-foreground flex items-baseline gap-1 text-xs font-normal'
            >
              <span>{t('Used amount')}</span>
              <span>{formattedUsed}</span>
            </span>
          </span>
        ) : (
          <StatusBadge
            label={t('No Quota')}
            variant='neutral'
            copyable={false}
            className='-ml-1.5 font-normal'
          />
        )}
      </QuotaDetailsPopover>
      <div className='space-y-2 border-t pt-2'>
        <div className='flex flex-wrap items-center justify-between gap-x-2 gap-y-1'>
          <span className='text-muted-foreground text-xs'>
            {t('Subscription usage')}
          </span>
          {props.onManageSubscriptions && (
            <Button
              variant='ghost'
              size='xs'
              className='h-auto py-0.5 text-xs'
              onClick={props.onManageSubscriptions}
            >
              {t('Manage Subscriptions')}
            </Button>
          )}
        </div>
        {props.subscriptions === undefined && (
          <p className='text-muted-foreground text-xs'>
            {t('Subscription usage unavailable')}
          </p>
        )}
        {props.subscriptions?.length === 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('No active subscriptions')}
          </p>
        )}
        {props.subscriptions?.slice(0, 2).map((subscription) => (
          <div key={subscription.id} className='min-w-0 space-y-1 text-xs'>
            <div className='flex min-w-0 flex-wrap items-center justify-between gap-x-2 gap-y-1'>
              <span
                className='min-w-0 truncate font-medium'
                title={subscription.plan_title || `#${subscription.plan_id}`}
              >
                {subscription.plan_title || `#${subscription.plan_id}`}
              </span>
              <span className='shrink-0 tabular-nums'>
                {formatQuota(subscription.amount_used)} /{' '}
                {subscription.amount_total > 0
                  ? formatQuota(subscription.amount_total)
                  : t('Unlimited')}
              </span>
            </div>
            <div className='text-muted-foreground flex flex-wrap gap-x-1'>
              {subscription.next_reset_time > 0 ? (
                <>
                  <span>{t('Next reset')}:</span>
                  <span className='tabular-nums'>
                    {formatTimestamp(subscription.next_reset_time)}
                  </span>
                </>
              ) : (
                t('No automatic reset')
              )}
            </div>
          </div>
        ))}
        {hiddenSubscriptionCount > 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('+{{count}} more subscriptions', {
              count: hiddenSubscriptionCount,
            })}
          </p>
        )}
      </div>
    </div>
  )
}
