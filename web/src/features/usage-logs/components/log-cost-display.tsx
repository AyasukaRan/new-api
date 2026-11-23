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
      className={cn(
        'col-start-2 row-start-1 h-auto min-h-5 gap-0 rounded-sm border-0 p-0 font-sans text-[13px] leading-5',
        toolCounts.size > 0
          ? 'col-end-4 grid grid-cols-subgrid'
          : 'justify-self-end'
      )}
    >
      <span className='justify-self-end font-semibold whitespace-nowrap tabular-nums underline decoration-current/40 decoration-dotted underline-offset-4'>
        {formatLogQuota(quota)}
      </span>
      {toolCounts.size > 0 && (
        <span
          data-fee-tool-lanes={Math.min(toolCounts.size, 3)}
          className={cn(
            'ms-1 grid shrink-0 items-center justify-self-start gap-0.5',
            toolCounts.size > 3 ? 'grid-cols-3' : 'grid-flow-col'
          )}
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
        </span>
      )}
    </Button>
  )
  return (
    <TooltipProvider>
      <div className='grid w-full grid-cols-[1.1875rem_minmax(max-content,1fr)_var(--log-fee-tool-track,max-content)] items-center gap-y-0.5 self-center font-sans tabular-nums'>
        {source && (
          <Tooltip>
            <TooltipTrigger
              render={
                <span
                  className='text-muted-foreground col-start-1 row-start-1 inline-flex w-3.5 shrink-0 cursor-help items-center justify-center'
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
        {(free > 0 || props.isAdmin) && (
          <dl className='text-muted-foreground col-span-2 col-start-1 grid grid-cols-subgrid items-center gap-y-0.5 text-end text-xs leading-4 font-normal [&_dd]:whitespace-nowrap [&_dt]:justify-start'>
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
