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
import { Brain, FlaskConical, Terminal, Unlink, Wrench } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { BadgeListCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getReasoningEffortVariant } from '../lib/format'
import { getLogToolIcon } from '../lib/tool-icon'
import type { LogOtherData } from '../types'
import { LogMetricPopover } from './log-metric-popover'
import { LogFilterValue } from './log-value-filter'

/** Only observed calls and explicit legacy per-call counters identify an invocation count. */
function toolInvocationCount(name: string, metadata: LogOtherData) {
  const observed = metadata.invoked_tool_counts?.[name]
  if (
    typeof observed === 'number' &&
    Number.isSafeInteger(observed) &&
    observed > 0
  ) {
    return observed
  }
  let legacy: number | undefined
  if (name === 'web_search') legacy = metadata.web_search_call_count
  if (name === 'file_search') legacy = metadata.file_search_call_count
  if (name === 'image_generation') legacy = metadata.image_generation_call_count
  return typeof legacy === 'number' &&
    Number.isSafeInteger(legacy) &&
    legacy > 0
    ? legacy
    : undefined
}

function ToolCallPopover(props: {
  names: string[]
  label: string
  metadata: LogOtherData
  children: ReactNode
}) {
  const { t, i18n } = useTranslation()
  return (
    <LogMetricPopover
      label={t('Tools')}
      className='w-64'
      trigger={
        <Button
          variant='ghost'
          size='sm'
          aria-label={props.label}
          className='bg-muted text-muted-foreground hover:text-foreground size-[26px] shrink-0 rounded-sm p-0 text-xs'
        >
          {props.children}
        </Button>
      }
    >
      <dl className='space-y-1.5'>
        {props.names.map((name) => {
          const count = toolInvocationCount(name, props.metadata)
          const Icon = getLogToolIcon(name)
          return (
            <div
              key={name}
              className='grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-3'
            >
              <dt className='flex min-w-0 items-start gap-1.5'>
                <Icon
                  aria-hidden
                  className='text-muted-foreground mt-px size-3.5 shrink-0'
                />
                <span className='font-mono break-all'>{name}</span>
              </dt>
              <dd className='text-muted-foreground text-right tabular-nums'>
                <span className='sr-only'>{t('Count')} </span>
                {count === undefined
                  ? t('Not recorded')
                  : `×${formatNumber(count, toIntlLocale(i18n.language))}`}
              </dd>
            </div>
          )
        })}
      </dl>
    </LogMetricPopover>
  )
}

export type RequestMetadataField =
  | 'reasoning_effort'
  | 'client_tool'
  | 'invoked_tools'

const fieldLabels: Record<RequestMetadataField, string> = {
  reasoning_effort: 'Reasoning Effort',
  client_tool: 'Client',
  invoked_tools: 'Tools',
}

export function RequestPurposeTag(props: {
  isAdmin: boolean
  metadata?: LogOtherData | null
}) {
  const { t } = useTranslation()
  if (!props.isAdmin || props.metadata?.request_purpose !== 'test') return null
  return (
    <Tooltip>
      <TooltipTrigger
        render={<span role='img' tabIndex={0} />}
        aria-label={t('Test request')}
        className='text-muted-foreground focus-visible:ring-ring inline-flex size-5 shrink-0 cursor-help items-center justify-center rounded-sm focus-visible:ring-2 focus-visible:outline-none'
      >
        <FlaskConical aria-hidden className='size-3.5' />
      </TooltipTrigger>
      <TooltipContent role='tooltip'>{t('Test request')}</TooltipContent>
    </Tooltip>
  )
}

export function RequestMetadataTags(props: {
  isAdmin: boolean
  metadata?: LogOtherData | null
  expanded?: boolean
  field?: RequestMetadataField
}) {
  const { t } = useTranslation()
  const metadata = props.metadata
  const showEffort = !props.field || props.field === 'reasoning_effort'
  const showClient = !props.field || props.field === 'client_tool'
  const showTools = !props.field || props.field === 'invoked_tools'
  const effort =
    showEffort && typeof metadata?.reasoning_effort === 'string'
      ? metadata.reasoning_effort.trim()
      : ''
  const adminMetadata = props.isAdmin ? metadata : null
  const client =
    showClient && typeof adminMetadata?.client_tool === 'string'
      ? adminMetadata.client_tool.trim()
      : ''
  const testRequest = showClient && adminMetadata?.request_purpose === 'test'
  const tools =
    showTools && Array.isArray(adminMetadata?.invoked_tools)
      ? [
          ...new Set(
            adminMetadata.invoked_tools
              .filter((name) => typeof name === 'string' && name.trim())
              .map((name) => name.trim())
          ),
        ]
      : []
  const partial = showTools && adminMetadata?.tool_observation === 'partial'
  const noTools =
    showTools &&
    props.expanded &&
    adminMetadata?.tool_observation === 'complete' &&
    Array.isArray(adminMetadata.invoked_tools) &&
    adminMetadata.invoked_tools.length === 0

  if (props.field === 'reasoning_effort' && effort) {
    const variant = getReasoningEffortVariant(effort)
    return (
      <LogFilterValue field='reasoningEffort' value={effort}>
        <StatusBadge
          label={effort}
          title={t('Reasoning: {{effort}}', { effort })}
          copyable={false}
          className={cn(
            'h-5 min-w-[7.5ch] max-w-full justify-center rounded-sm px-1.5 font-mono text-[11px] font-semibold',
            variant === 'orange' &&
              'bg-amber-100 text-amber-800 dark:bg-amber-400/15 dark:text-amber-200',
            variant === 'yellow' &&
              'bg-violet-100 text-violet-800 dark:bg-violet-300/15 dark:text-violet-200',
            variant === 'green' &&
              'bg-emerald-100 text-emerald-800 dark:bg-emerald-300/15 dark:text-emerald-200',
            variant === 'grey' && 'bg-muted text-muted-foreground'
          )}
        />
      </LogFilterValue>
    )
  }

  if (
    !effort &&
    !client &&
    !testRequest &&
    tools.length === 0 &&
    !partial &&
    !noTools
  ) {
    return null
  }

  const incompleteTools = partial && (
    <Tooltip>
      <TooltipTrigger
        render={<span role='img' tabIndex={0} />}
        aria-label={t('Tool details incomplete')}
        className='text-warning focus-visible:ring-ring inline-flex size-6 shrink-0 cursor-help items-center justify-center rounded-sm focus-visible:ring-2 focus-visible:outline-none'
      >
        <Unlink aria-hidden className='size-4' />
      </TooltipTrigger>
      <TooltipContent role='tooltip'>
        {t('Tool details incomplete')}
      </TooltipContent>
    </Tooltip>
  )

  if (
    props.field === 'invoked_tools' &&
    !props.expanded &&
    adminMetadata &&
    (tools.length > 0 || partial)
  ) {
    return (
      <div
        className='flex items-center justify-center gap-1'
        aria-label={t('Tools')}
      >
        {tools.slice(0, 3).map((name) => {
          const Icon = getLogToolIcon(name)
          return (
            <ToolCallPopover
              key={name}
              names={[name]}
              label={t('Tool: {{name}}', { name })}
              metadata={adminMetadata}
            >
              <Icon aria-hidden className='size-[15px]' />
              <span className='sr-only'>{name}</span>
            </ToolCallPopover>
          )
        })}
        {tools.length > 3 && (
          <ToolCallPopover
            names={tools}
            label={t('Show all invoked tools')}
            metadata={adminMetadata}
          >
            +{tools.length - 3}
          </ToolCallPopover>
        )}
        {incompleteTools}
      </div>
    )
  }

  const toolTags = tools.map((name) => (
    <StatusBadge
      key={name}
      label={t('Tool: {{name}}', { name })}
      variant='teal'
      className='h-auto min-h-5 shrink-0'
      icon={Wrench}
      copyable={false}
    >
      <span className='min-w-0 break-all whitespace-normal'>
        {props.field ? name : t('Tool: {{name}}', { name })}
      </span>
    </StatusBadge>
  ))

  return (
    <div
      role='group'
      aria-label={
        props.field ? t(fieldLabels[props.field]) : t('Request attributes')
      }
      className={cn(
        'flex max-w-full min-w-0 flex-wrap items-center gap-1',
        props.field &&
          'max-w-[min(100%,10rem)] sm:[&_[data-slot=status-badge]>svg]:hidden'
      )}
    >
      {effort && (
        <StatusBadge
          label={props.field ? effort : t('Reasoning: {{effort}}', { effort })}
          title={t('Reasoning: {{effort}}', { effort })}
          variant={getReasoningEffortVariant(effort)}
          icon={Brain}
          copyable={false}
        />
      )}
      {client && (
        <StatusBadge
          label={client}
          title={t('Client: {{client}}', { client })}
          variant='blue'
          icon={Terminal}
          copyable={false}
        />
      )}
      {testRequest && (
        <RequestPurposeTag isAdmin={props.isAdmin} metadata={metadata} />
      )}
      {toolTags.length > 0 &&
        (props.expanded ? (
          toolTags
        ) : (
          <div
            data-slot='request-tool-tags'
            className='w-full max-w-full min-w-0 [&_[data-slot=status-badge]>span]:truncate [&>div]:flex-wrap [&>div>div]:flex-wrap'
          >
            <BadgeListCell
              items={toolTags}
              max={props.field ? 1 : 2}
              expandable
              expandLabel={t('Show all invoked tools')}
            />
          </div>
        ))}
      {incompleteTools}
      {noTools && (
        <span className='text-muted-foreground text-xs'>
          {t('No tools invoked')}
        </span>
      )}
    </div>
  )
}
