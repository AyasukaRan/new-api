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

import { normalizeTierLabel } from '@/features/pricing/lib/billing-expr'
import { toIntlLocale } from '@/i18n/languages'
import { getCurrencyDisplay } from '@/lib/currency'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import {
  formatReceiptQuota,
  getBilledTools,
  getChargeBreakdown,
} from '../lib/charge-breakdown'
import {
  getTieredBillingSummary,
  getLogCostEstimate,
  getLogFreeQuota,
} from '../lib/format'
import type { LogOtherData } from '../types'
import { BillingTierIcon } from './billing-tier-icon'
import { LogBreakdownBar } from './log-breakdown-bar'
import { LogGroupLabel } from './log-identity-cells'

const chargeColors: Record<string, string> = {
  Input: 'bg-sky-500',
  'Uncached input': 'bg-sky-500',
  Output: 'bg-violet-500',
  'Cache Read': 'bg-teal-500',
  'Cache Write': 'bg-amber-500',
  'Cache Write (1h)': 'bg-yellow-400',
  'Cache Write (12h)': 'bg-orange-400',
  web_search: 'bg-emerald-400',
  file_search: 'bg-fuchsia-400',
  image_generation: 'bg-orange-400',
}

export function LogChargeDetails({
  log,
  other,
  isAdmin,
  compact = false,
  quota = log?.quota ?? 0,
}: {
  log?: UsageLog
  other: LogOtherData | null
  isAdmin?: boolean
  compact?: boolean
  quota?: number
}) {
  const { t, i18n } = useTranslation()
  const tools = getBilledTools(other)
  const free = getLogFreeQuota(other, quota)
  let payable = quota
  if (free > 0) {
    payable -= free
  } else if (other?.billing_source === 'subscription') {
    payable = other.subscription_consumed ?? quota
  }
  const cost = isAdmin ? getLogCostEstimate(other) : null
  const { config } = getCurrencyDisplay()
  const lines = getChargeBreakdown(
    other,
    quota,
    config.quotaPerUnit,
    tools,
    log
  ).map(({ quota: value, ...part }) => ({
    ...part,
    key: `${part.tool ?? false}:${part.label}:${part.basis?.unitPriceQuota}`,
    label: part.tool ? part.label : t(part.label),
    color:
      chargeColors[part.label] ?? (part.tool ? 'bg-cyan-400' : 'bg-slate-400'),
    value,
  }))
  const rawTotal = lines.reduce((sum, line) => sum + line.value, 0)
  const rounding = quota - rawTotal
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const percentage = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 1,
  })
  const tier = normalizeTierLabel(other?.matched_tier)
  let tariff: string | undefined
  if (tier === 'peak') tariff = 'Peak hours'
  if (tier === 'off_peak') tariff = 'Off-peak hours'
  if (tier === 'base' || tier === 'standard') tariff = 'Standard'
  const rateSummary =
    compact || lines.length ? null : getTieredBillingSummary(other, log?.model_name)

  return (
    <div className={compact ? 'space-y-2 text-xs' : 'space-y-4 text-sm'}>
      <div className='flex items-baseline justify-between gap-2'>
        <span className='text-muted-foreground'>{t('Amount charged')}</span>
        <strong
          className={cn(
            'font-mono font-semibold tabular-nums',
            compact ? 'text-base' : 'text-xl'
          )}
        >
          {formatReceiptQuota(payable)}
        </strong>
      </div>
      {(log?.group || other?.group_ratio != null || tier) && (
        <div className='flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs'>
          <LogGroupLabel group={log?.group ?? ''} other={other} visible />
          {other?.matched_tier && (
            <span className='text-muted-foreground inline-flex items-center gap-1'>
              {tariff && (
                <BillingTierIcon
                  tier={
                    tier === 'off_peak' || tier === 'peak' ? tier : 'standard'
                  }
                  label={t(tariff)}
                />
              )}
              {tariff
                ? t(tariff)
                : t('Pricing tier {{name}}', { name: other.matched_tier })}
            </span>
          )}
        </div>
      )}
      {lines.length > 0 && (
        <>
          {payable !== quota && (
            <div className='text-muted-foreground flex items-baseline justify-between gap-2 text-xs'>
              <span>{t('Total Cost')}</span>
              <span className='font-mono tabular-nums'>
                {formatReceiptQuota(quota)}
              </span>
            </div>
          )}
          {quota > 0 && rawTotal > 0 && (
            <LogBreakdownBar
              label={t('Cost breakdown')}
              segments={lines}
              total={rawTotal}
            />
          )}
          <ul
            aria-label={t('Cost breakdown')}
            className='divide-border divide-y'
          >
            {lines.map((part) => (
              <li
                key={part.key}
                className={cn(
                  'grid grid-cols-[0.5rem_minmax(0,1fr)_auto] items-baseline gap-x-2 gap-y-1',
                  compact ? 'py-1.5' : 'py-2'
                )}
              >
                <span
                  aria-hidden
                  className={cn('size-2 rounded-full', part.color)}
                />
                <span className='min-w-0 font-medium break-all'>
                  {part.label}
                  {compact && part.tool && part.basis && (
                    <span className='text-muted-foreground ms-1 font-mono text-xs whitespace-nowrap'>
                      ×{formatNumber(part.basis.count, locale)}
                    </span>
                  )}
                </span>
                <span className='text-end font-mono font-medium tabular-nums'>
                  {formatReceiptQuota(part.value)}
                </span>
                {!compact && (
                  <>
                    <span className='text-muted-foreground col-start-2 font-mono text-xs tabular-nums'>
                      {part.basis && (
                        <>
                          {formatNumber(part.basis.count, locale)}{' '}
                          {part.unit && t(part.unit)}
                          {' × '}
                          {formatReceiptQuota(part.basis.unitPriceQuota)} /{' '}
                          {formatNumber(part.basis.per, locale)}{' '}
                          {part.unit && t(part.unit)}
                          {part.basis.multipliers
                            .map((factor) => ` × ${factor}`)
                            .join('')}
                        </>
                      )}
                    </span>
                    <span className='text-muted-foreground text-end font-mono text-xs tabular-nums'>
                      {rawTotal > 0 &&
                        (part.value > 0 && part.value / rawTotal < 0.001
                          ? `<${percentage.format(0.001)}`
                          : percentage.format(part.value / rawTotal))}
                    </span>
                  </>
                )}
              </li>
            ))}
            {Math.abs(rounding) > 0.000001 && (
              <li className='text-muted-foreground flex justify-between gap-3 py-2 text-xs'>
                <span>{t('Settlement rounding')}</span>
                <span className='font-mono tabular-nums'>
                  {formatReceiptQuota(rounding)}
                </span>
              </li>
            )}
          </ul>
        </>
      )}
      {!lines.length && (
        <>
          <p className='text-muted-foreground text-xs'>
            {t(
              'A reconciled itemized breakdown is unavailable for this record.'
            )}
          </p>
          {rateSummary?.priceEntries.map((entry) => (
            <div
              key={entry.field}
              className='flex items-baseline justify-between gap-3 text-xs'
            >
              <span>{t(entry.shortLabel)}</span>
              <span className='text-muted-foreground font-mono'>
                {formatReceiptQuota(
                  entry.price *
                    (other?.billing_quota_per_unit ?? config.quotaPerUnit)
                )}{' '}
                {entry.unit ? `/ ${t(entry.unit)}` : t('Per 1M tokens')}
              </span>
            </div>
          ))}
          {tools.map((tool) => (
            <div
              key={`${tool.name}:${tool.price}`}
              className='flex flex-wrap items-baseline justify-between gap-1'
            >
              <span className='break-all'>{tool.name}</span>
              <span className='text-muted-foreground font-mono text-xs'>
                {compact ? (
                  `×${formatNumber(tool.count, locale)}`
                ) : (
                  <>
                    {formatNumber(tool.count, locale)} ×{' '}
                    {formatReceiptQuota(tool.price * config.quotaPerUnit)} /{' '}
                    {formatNumber(tool.per, locale)}
                  </>
                )}
              </span>
            </div>
          ))}
        </>
      )}
      {(free > 0 || payable !== quota || cost) && (
        <dl className='border-border grid grid-cols-[1fr_auto] gap-x-4 gap-y-2 border-t pt-3 [&_dd]:text-end [&_dd]:font-mono [&_dd]:tabular-nums'>
          {free > 0 && (
            <>
              <dt>{t('Free allowance deduction')}</dt>
              <dd>−{formatLogQuota(free)}</dd>
            </>
          )}
          {!lines.length && payable !== quota && (
            <>
              <dt>{t('Total Cost')}</dt>
              <dd>{formatLogQuota(quota)}</dd>
            </>
          )}
          {cost && (
            <>
              <dt>{t('Billed usage')}</dt>
              <dd>{formatLogQuota(cost.revenue_quota)}</dd>
              <dt>
                {t(
                  cost.estimated ? 'Estimated upstream cost' : 'Upstream cost'
                )}
              </dt>
              <dd>{formatLogQuota(cost.cost_quota)}</dd>
              <dt>{t('Gross profit')}</dt>
              <dd>
                {!compact &&
                  Math.abs(
                    cost.revenue_quota - cost.cost_quota - cost.profit_quota
                  ) < 0.000001 && (
                    <>
                      {formatLogQuota(cost.revenue_quota)} −{' '}
                      {formatLogQuota(cost.cost_quota)} ={' '}
                    </>
                  )}
                {formatLogQuota(cost.profit_quota)}
              </dd>
            </>
          )}
        </dl>
      )}
    </div>
  )
}
