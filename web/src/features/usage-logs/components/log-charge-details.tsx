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
import { LogGroupLabel } from './log-identity-cells'

const chargeColors: Record<string, string> = {
  Input: 'bg-blue-400',
  'Uncached input': 'bg-blue-400',
  Output: 'bg-violet-400',
  'Cache Read': 'bg-emerald-400',
  'Cache Write': 'bg-amber-500',
  'Cache Write (1h)': 'bg-yellow-400',
  'Cache Write (12h)': 'bg-orange-400',
  web_search: 'bg-teal-400',
  file_search: 'bg-fuchsia-400',
  image_generation: 'bg-orange-400',
}

export function LogChargeDetails({
  log,
  other,
  isAdmin,
  quota = log?.quota ?? 0,
}: {
  log?: UsageLog
  other: LogOtherData | null
  isAdmin?: boolean
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
  )
  const rawTotal = lines.reduce((sum, line) => sum + line.quota, 0)
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
  const rateSummary = lines.length ? null : getTieredBillingSummary(other)

  return (
    <div className='space-y-4 text-sm'>
      <div className='flex items-baseline justify-between gap-2'>
        <span className='text-muted-foreground'>{t('Amount charged')}</span>
        <strong className='font-mono text-xl font-semibold tabular-nums'>
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
          {quota > 0 && (
            <div
              className='bg-muted flex h-2 w-full overflow-hidden rounded-full'
              aria-hidden='true'
            >
              {lines.map((part) => (
                <span
                  key={`${part.tool ?? false}:${part.label}:${part.basis?.unitPriceQuota}`}
                  className={
                    chargeColors[part.label] ??
                    (part.tool ? 'bg-cyan-400' : 'bg-slate-400')
                  }
                  style={{ flexGrow: part.quota, flexBasis: 0 }}
                />
              ))}
            </div>
          )}
          <ul
            aria-label={t('Cost breakdown')}
            className='divide-border divide-y'
          >
            {lines.map((part) => (
              <li
                key={`${part.tool ?? false}:${part.label}:${part.basis?.unitPriceQuota}`}
                className='grid grid-cols-[0.5rem_minmax(0,1fr)_auto] items-baseline gap-x-2 gap-y-1 py-2'
              >
                <span
                  aria-hidden
                  className={cn(
                    'size-2 rounded-full',
                    chargeColors[part.label] ??
                      (part.tool ? 'bg-cyan-400' : 'bg-slate-400')
                  )}
                />
                <span className='min-w-0 font-medium break-all'>
                  {part.tool ? part.label : t(part.label)}
                </span>
                <span className='text-end font-mono font-medium tabular-nums'>
                  {formatReceiptQuota(part.quota)}
                </span>
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
                    (part.quota > 0 && part.quota / rawTotal < 0.001
                      ? `<${percentage.format(0.001)}`
                      : percentage.format(part.quota / rawTotal))}
                </span>
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
                {formatNumber(tool.count)} ×{' '}
                {formatReceiptQuota(tool.price * config.quotaPerUnit)} /{' '}
                {formatNumber(tool.per)}
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
          {(free > 0 || payable !== quota) && (
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
                {Math.abs(
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
