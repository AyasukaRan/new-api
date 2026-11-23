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
import { formatLogQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../data/schema'
import { getBilledTools } from '../lib/charge-breakdown'
import { getLogCostEstimate, getLogFreeQuota } from '../lib/format'
import { getLogToolIcon } from '../lib/tool-icon'
import type { LogOtherData } from '../types'
import { LogChargeDetails } from './log-charge-details'
import { renderLogMetricTerm } from './log-metric-icons'
import { LogMetricsDetailDialog } from './log-metrics-detail-dialog'

interface LogCostDisplayProps {
  quota: number
  other: LogOtherData | null
  log?: UsageLog
  showBillingSource?: boolean
  isAdmin?: boolean
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

  const amountButton = (
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
  )
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
          {props.log ? (
            <LogMetricsDetailDialog
              log={props.log}
              other={props.other}
              isAdmin={Boolean(props.isAdmin)}
              initialTab='cost'
              trigger={amountButton}
            />
          ) : (
            <Dialog
              open={open}
              onOpenChange={setOpen}
              title={t('Billing Details')}
              contentClassName='sm:max-w-md'
              trigger={amountButton}
            >
              {open && <LogChargeDetails {...props} />}
            </Dialog>
          )}
        </div>
        {(free > 0 || props.isAdmin) && (
          <dl className='text-muted-foreground grid w-max max-w-full grid-cols-[max-content_max-content] items-center gap-x-[5px] gap-y-0.5 text-[11px] leading-tight [&_dd]:text-start [&_dd]:font-mono [&_dd]:whitespace-nowrap [&_dd]:tabular-nums [&_dt]:text-start'>
            {free > 0 && (
              <>
                {renderLogMetricTerm('Free allowance deduction', t)}
                <dd>{formatLogQuota(free)}</dd>
              </>
            )}
            {props.isAdmin && (
              <>
                {renderLogMetricTerm('Upstream cost', t)}
                <dd>
                  {cost ? formatLogQuota(cost.cost_quota) : t('Not recorded')}
                </dd>
                {renderLogMetricTerm('Gross profit', t)}
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
