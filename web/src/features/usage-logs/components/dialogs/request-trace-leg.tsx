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
import { ChevronDown, Wrench } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CodeBlock } from '@/components/ai-elements/code-block'
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from '@/components/ai-elements/reasoning'
import { Response } from '@/components/ai-elements/response'
import { ToolContent, ToolInput } from '@/components/ai-elements/tool'
import { StatusBadge } from '@/components/status-badge'
import { Collapsible, CollapsibleTrigger } from '@/components/ui/collapsible'

import type { RequestTraceLeg, RequestTraceToolCall } from '../../types'
import { RequestTraceObject } from './request-trace-object'

/** Pretty-prints a JSON payload, leaving anything unparseable untouched. */
function formatPayload(body: string): { code: string; language: string } {
  const trimmed = body.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) {
    return { code: body, language: 'text' }
  }
  try {
    return {
      code: JSON.stringify(JSON.parse(trimmed), null, 2),
      language: 'json',
    }
  } catch {
    // A truncated payload is no longer valid JSON but is still worth reading.
    return { code: body, language: 'json' }
  }
}

export function RequestTraceRawLeg(props: {
  traceId: string
  leg: RequestTraceLeg
  view: 'body' | 'headers'
}) {
  const { t } = useTranslation()
  if (props.view === 'headers') {
    if (!props.leg.headers || Object.keys(props.leg.headers).length === 0) {
      return (
        <p className='text-muted-foreground text-sm'>
          {t('No headers were captured for this exchange.')}
        </p>
      )
    }
    return (
      <CodeBlock
        code={JSON.stringify(props.leg.headers, null, 2)}
        language='json'
        title={t('Headers')}
        showToolbar
        maxExpandedLines={40}
      />
    )
  }
  const payload = formatPayload(props.leg.body)
  return (
    <div className='min-w-0 space-y-3'>
      {props.leg.has_object && (
        <RequestTraceObject traceId={props.traceId} leg={props.leg} />
      )}
      {props.leg.body && (
        <CodeBlock
          code={payload.code}
          language={payload.language}
          title={t('Body')}
          showToolbar
          showLineNumbers
          collapsedLines={20}
          maxExpandedLines={40}
        />
      )}
      {!props.leg.body && !props.leg.has_object && (
        <p className='text-muted-foreground text-sm'>
          {t('No body was captured for this leg.')}
        </p>
      )}
    </div>
  )
}

/**
 * A recorded tool call is a request, not a completed invocation: the trace has
 * no result and no lifecycle state. ToolHeader would have to be given one and
 * would label it "Running" or "Completed", so the header is composed here while
 * the argument rendering is reused from ToolInput.
 */
function RequestTraceToolCallItem(props: { call: RequestTraceToolCall }) {
  const { t } = useTranslation()
  let parsedArguments: unknown = props.call.arguments ?? ''
  try {
    parsedArguments = JSON.parse(props.call.arguments || '{}')
  } catch {
    parsedArguments = props.call.arguments
  }

  return (
    <Collapsible className='not-prose w-full rounded-md border'>
      <CollapsibleTrigger className='group flex w-full items-center justify-between gap-4 p-3'>
        <span className='flex min-w-0 items-center gap-2'>
          <Wrench className='text-muted-foreground size-4' aria-hidden='true' />
          <span className='truncate text-sm font-medium'>
            {props.call.name || t('Unnamed tool')}
          </span>
        </span>
        <ChevronDown
          className='text-muted-foreground size-4 transition-transform group-data-[panel-open]:rotate-180'
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <ToolContent>
        <ToolInput input={parsedArguments} />
      </ToolContent>
    </Collapsible>
  )
}

export function RequestTraceRenderedLeg(props: {
  leg: RequestTraceLeg
  label: string
  showAttempt: boolean
}) {
  const { t } = useTranslation()
  const rendered = props.leg.rendered
  if (!rendered) return null

  return (
    <div className='space-y-2'>
      <div className='flex flex-wrap items-center gap-1.5 text-xs'>
        {/* Both response legs are parsed, and they differ whenever new-api
            converted formats, so the reader has to know which side this is. */}
        <StatusBadge
          label={t(props.label)}
          variant='neutral'
          size='sm'
          copyable={false}
        />
        {props.showAttempt && (
          <StatusBadge
            label={t('Attempt {{index}}', { index: props.leg.attempt + 1 })}
            variant='neutral'
            size='sm'
            copyable={false}
          />
        )}
        {rendered.finish_reason && (
          <StatusBadge
            label={rendered.finish_reason}
            variant='neutral'
            size='sm'
            copyable={false}
          />
        )}
        {rendered.stream && (
          <StatusBadge
            label={t('Stream')}
            variant='info'
            size='sm'
            copyable={false}
          />
        )}
      </div>

      {rendered.reasoning && (
        // defaultOpen={false} on purpose: with isStreaming off, an open panel
        // auto-collapses a second after mount, which reads as a glitch.
        <Reasoning isStreaming={false} defaultOpen={false}>
          {/* The default trigger reports a thinking duration, which a stored
              trace does not have and would render as "0 seconds". */}
          <ReasoningTrigger className='group inline-flex whitespace-nowrap'>
            {t('Reasoning')}
            <ChevronDown
              className='size-3.5 group-data-[panel-open]:rotate-180'
              aria-hidden='true'
            />
          </ReasoningTrigger>
          <ReasoningContent>{rendered.reasoning}</ReasoningContent>
        </Reasoning>
      )}

      {rendered.content && <Response final>{rendered.content}</Response>}

      {rendered.tool_calls?.map((call, index) => (
        <RequestTraceToolCallItem
          key={call.id || `${call.name}-${index}`}
          call={call}
        />
      ))}
    </div>
  )
}
