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

import { getReasoningEffortVariant } from '../lib/format'
import type { LogOtherData } from '../types'

export function RequestMetadataTags(props: {
  metadata?: LogOtherData | null
  expanded?: boolean
}) {
  const { t } = useTranslation()
  const metadata = props.metadata
  const effort =
    typeof metadata?.reasoning_effort === 'string'
      ? metadata.reasoning_effort.trim()
      : ''
  const client =
    typeof metadata?.client_tool === 'string' ? metadata.client_tool.trim() : ''
  const tools = Array.isArray(metadata?.invoked_tools)
    ? [
        ...new Set(
          metadata.invoked_tools
            .filter((name) => typeof name === 'string' && name.trim())
            .map((name) => name.trim())
        ),
      ]
    : []
  const partial = metadata?.tool_observation === 'partial'
  const noTools =
    props.expanded &&
    metadata?.tool_observation === 'complete' &&
    Array.isArray(metadata.invoked_tools) &&
    metadata.invoked_tools.length === 0

  if (!effort && !client && tools.length === 0 && !partial && !noTools) {
    return null
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
        {t('Tool: {{name}}', { name })}
      </span>
    </StatusBadge>
  ))

  return (
    <div
      role='group'
      aria-label={t('Request attributes')}
      className='flex max-w-full min-w-0 flex-wrap items-center gap-1'
    >
      {effort && (
        <StatusBadge
          label={t('Reasoning: {{effort}}', { effort })}
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
              max={2}
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
