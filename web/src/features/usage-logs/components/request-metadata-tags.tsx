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
import { Brain, Terminal, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { BadgeListCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import { getReasoningEffortVariant } from '../lib/format'
import { getLogToolIcon } from '../lib/tool-icon'
import type { LogOtherData } from '../types'

export type RequestMetadataField =
  | 'reasoning_effort'
  | 'client_tool'
  | 'invoked_tools'

const fieldLabels: Record<RequestMetadataField, string> = {
  reasoning_effort: 'Reasoning Effort',
  client_tool: 'Client',
  invoked_tools: 'Tools',
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

  if (!effort && !client && tools.length === 0 && !partial && !noTools) {
    return null
  }

  if (props.field === 'invoked_tools' && !props.expanded && tools.length > 0) {
    const icons = tools.map((name) => {
      const Icon = getLogToolIcon(name)
      return (
        <span
          key={name}
          title={name}
          className='bg-muted text-muted-foreground inline-flex size-6 items-center justify-center rounded'
        >
          <Icon aria-hidden className='size-3.5' />
          <span className='sr-only'>{name}</span>
        </span>
      )
    })
    return (
      <div
        className='flex items-center justify-center gap-1'
        aria-label={t('Tools')}
      >
        <BadgeListCell
          items={icons}
          max={3}
          expandable
          expandLabel={t('Show all invoked tools')}
        />
        {partial && (
          <span
            title={t('Tool details incomplete')}
            aria-label={t('Tool details incomplete')}
          >
            …
          </span>
        )}
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
      {partial && (
        <StatusBadge
          label={t('Tool details incomplete')}
          variant='orange'
          copyable={false}
        />
      )}
      {noTools && (
        <span className='text-muted-foreground text-xs'>
          {t('No tools invoked')}
        </span>
      )}
    </div>
  )
}
