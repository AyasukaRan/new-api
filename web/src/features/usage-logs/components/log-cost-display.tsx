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
import { CrownIcon, Wallet01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getCurrencyDisplay } from '@/lib/currency'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import {
  formatReceiptQuota,
  getBilledTools,
  getChargeBreakdown,
  type BilledTool,
} from '../lib/charge-breakdown'
import { getLogCostEstimate, getLogFreeQuota } from '../lib/format'
import { getLogToolIcon } from '../lib/tool-icon'
import type { LogOtherData } from '../types'

interface LogCostDisplayProps {
  quota: number
  other: LogOtherData | null
  log?: UsageLog
  showBillingSource?: boolean
  isAdmin?: boolean
}

const chargeColors = [
  'bg-blue-500',
  'bg-teal-500',
  'bg-amber-500',
  'bg-violet-500',
  'bg-cyan-500',
  'bg-rose-500',
]

function ChargeDetails({
  quota,
  other,
  log,
  isAdmin,
  payable,
  tools,
}: LogCostDisplayProps & { payable: number; tools: BilledTool[] }) {
  const { t } = useTranslation()
  const cost = isAdmin ? getLogCostEstimate(other) : null
  const free = getLogFreeQuota(other, quota)
  const { config } = getCurrencyDisplay()
  const lines = getChargeBreakdown(
    other,
    quota,
    config.quotaPerUnit,
    tools,
    log
  )
  const rounding = quota - lines.reduce((sum, line) => sum + line.quota, 0)
  return (
    <div className='space-y-4 text-sm'>
      <div className='flex items-baseline justify-between'>
        <span>{t('Total Cost')}</span>
        <strong className='font-mono tabular-nums'>
          {formatLogQuota(quota)}
        </strong>
      </div>
      {lines.length > 0 && (
        <>
          {quota > 0 && (
            <div
              className='bg-muted flex h-2 w-full overflow-hidden rounded-full'
              aria-hidden='true'
            >
              {lines.map((part, i) => (
                <span
                  key={`${part.tool ?? false}:${part.label}:${part.formula}`}
                  className={chargeColors[i % chargeColors.length]}
                  style={{ flexGrow: part.quota, flexBasis: 0 }}
                />
              ))}
            </div>
          )}
          <ul className='divide-border divide-y'>
            {lines.map((part, i) => (
              <li
                key={`${part.tool ?? false}:${part.label}:${part.formula}`}
                className='flex items-start justify-between gap-3 py-2'
              >
                <div className='flex min-w-0 items-baseline gap-2'>
                  <span
                    aria-hidden='true'
                    className={cn(
                      'size-2 shrink-0 rounded-full',
                      chargeColors[i % chargeColors.length]
                    )}
                  />
                  <div className='min-w-0'>
                    <span className='font-medium break-all'>
                      {part.tool ? part.label : t(part.label)}
                    </span>
                    {part.formula && (
                      <p className='text-muted-foreground mt-0.5 font-mono text-xs tabular-nums'>
                        {part.formula}
                      </p>
                    )}
                  </div>
                </div>
                <span className='shrink-0 font-mono tabular-nums'>
                  {formatReceiptQuota(part.quota)}
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
              <dt>{t('Amount Due')}</dt>
              <dd>{formatLogQuota(payable)}</dd>
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

export function LogCostDisplay(props: LogCostDisplayProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const cost = props.isAdmin ? getLogCostEstimate(props.other) : null
  const isSubscription = props.other?.billing_source === 'subscription'
  const free = getLogFreeQuota(props.other, props.quota)
  let quota = props.quota
  if (free > 0) {
    quota -= free
  } else if (isSubscription) {
    quota = props.other?.subscription_consumed ?? quota
  }
  let source: string | undefined
  if (isSubscription) {
    source = t('Subscription')
  } else if (
    props.showBillingSource &&
    props.other?.billing_source === 'wallet'
  ) {
    source = t('Wallet')
  }
  const tools = getBilledTools(props.other)
  const toolCounts = new Map<string, number>()
  for (const tool of tools) {
    toolCounts.set(tool.name, (toolCounts.get(tool.name) ?? 0) + tool.count)
  }

  return (
    <TooltipProvider>
      <div className='flex w-full flex-col items-end justify-center gap-0.5 self-center align-middle'>
        <div className='flex max-w-full items-center justify-end gap-1.5'>
          {source && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <span
                    className='text-muted-foreground inline-flex shrink-0 cursor-help items-center'
                    role='img'
                    aria-label={source}
                    tabIndex={0}
                  >
                    <HugeiconsIcon
                      icon={isSubscription ? CrownIcon : Wallet01Icon}
                      className='size-3.5'
                      strokeWidth={2}
                      aria-hidden='true'
                    />
                  </span>
                }
              />
              <TooltipContent>{source}</TooltipContent>
            </Tooltip>
          )}
          <Dialog
            open={open}
            onOpenChange={setOpen}
            title={t('Billing Details')}
            contentClassName='sm:max-w-md'
            trigger={
              <Button
                variant='ghost'
                onClick={(event) => event.stopPropagation()}
                aria-label={`${t('Billing Details')} · ${formatLogQuota(quota)}`}
                className='h-auto min-h-6 max-w-full gap-1.5 rounded-sm px-0 py-0.5 text-sm'
              >
                {Array.from(toolCounts, ([name, count]) => {
                  const Icon = getLogToolIcon(name)
                  return (
                    <span
                      key={name}
                      data-billed-tool={name}
                      data-tool-surcharge-indicator='true'
                      role='img'
                      aria-label={t('Tool: {{name}}', { name })}
                      className='relative inline-grid size-4 shrink-0 place-content-center text-teal-700 dark:text-teal-300'
                    >
                      {count > 1 && (
                        <Icon
                          aria-hidden='true'
                          className='absolute top-0 left-0 size-3 opacity-50'
                        />
                      )}
                      <Icon
                        aria-hidden='true'
                        className={cn(
                          'size-3.5',
                          count > 1 && 'translate-x-0.5 translate-y-0.5'
                        )}
                      />
                    </span>
                  )
                })}
                <span className='font-mono font-semibold whitespace-nowrap tabular-nums underline decoration-current/40 decoration-dotted underline-offset-4'>
                  {formatLogQuota(quota)}
                </span>
              </Button>
            }
          >
            {open && <ChargeDetails {...props} payable={quota} tools={tools} />}
          </Dialog>
        </div>
        {(free > 0 || props.isAdmin) && (
          <dl className='text-muted-foreground grid w-full grid-cols-[1fr_auto] items-baseline gap-x-2 gap-y-0.5 text-[11px] leading-tight [&_dd]:text-end [&_dd]:font-mono [&_dd]:whitespace-nowrap [&_dd]:tabular-nums [&_dt]:text-start'>
            {free > 0 && (
              <>
                <dt>{t('Free allowance deduction')}</dt>
                <dd>{formatLogQuota(free)}</dd>
              </>
            )}
            {props.isAdmin && (
              <>
                <dt>{t('Upstream cost')}</dt>
                <dd>
                  {cost ? formatLogQuota(cost.cost_quota) : t('Not recorded')}
                </dd>
                <dt>{t('Gross profit')}</dt>
                <dd
                  className={cn(
                    cost && cost.profit_quota < 0 && 'text-destructive'
                  )}
                >
                  {cost ? formatLogQuota(cost.profit_quota) : t('Not recorded')}
                </dd>
              </>
            )}
          </dl>
        )}
      </div>
    </TooltipProvider>
  )
}
