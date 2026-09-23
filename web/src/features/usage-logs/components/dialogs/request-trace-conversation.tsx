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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlock } from '@/components/ai-elements/code-block'
import { Message, MessageContent } from '@/components/ai-elements/message'
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from '@/components/ai-elements/reasoning'
import { Response } from '@/components/ai-elements/response'
import { Tool, ToolContent, ToolInput } from '@/components/ai-elements/tool'
import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'

import {
  buildRequestTraceConversation,
  type TraceConversationMessage,
  type TraceConversationPart,
} from '../../lib/request-trace-conversation'
import type { RequestTraceLeg } from '../../types'

interface TraceConversationEntry {
  id: string
  message: TraceConversationMessage
  parts: { id: string; part: TraceConversationPart }[]
}

function TraceToolPart(props: {
  part: Extract<TraceConversationPart, { type: 'tool-call' | 'tool-result' }>
}) {
  const { t } = useTranslation()
  const isCall = props.part.type === 'tool-call'
  const label = isCall ? t('Tool call') : t('Tool result')
  const result = isCall ? props.part.result : props.part
  const toolName = props.part.name ?? props.part.result?.name
  const code =
    result &&
    (typeof result.value === 'string'
      ? result.value
      : JSON.stringify(result.value, null, 2))

  return (
    <Tool className='mb-0'>
      {/* ToolHeader requires a live execution status; a trace records only what was sent. */}
      <CollapsibleTrigger
        aria-label={[label, toolName || t('Unnamed tool'), props.part.id]
          .filter(Boolean)
          .join(' ')}
        className='group flex w-full items-center justify-between gap-3 p-3 text-left'
      >
        <span className='flex min-w-0 flex-wrap items-center gap-2'>
          <Wrench
            className='text-muted-foreground size-4 shrink-0'
            aria-hidden='true'
          />
          <span className='text-muted-foreground text-xs'>{label}</span>
          <span className='text-sm font-medium break-all'>
            {toolName || t('Unnamed tool')}
          </span>
          {props.part.id && (
            <span className='text-muted-foreground font-mono text-xs break-all'>
              {props.part.id}
            </span>
          )}
          {result?.isError && (
            <StatusBadge
              label={t('Error')}
              variant='danger'
              size='sm'
              copyable={false}
            />
          )}
          {isCall && props.part.linked && (
            <StatusBadge
              label={t('Result recorded')}
              variant='neutral'
              size='sm'
              copyable={false}
            />
          )}
        </span>
        <ChevronDown
          className='text-muted-foreground size-4 shrink-0 transition-transform group-data-[panel-open]:rotate-180'
          aria-hidden='true'
        />
      </CollapsibleTrigger>
      <ToolContent>
        {isCall && <ToolInput input={props.part.value} />}
        {/* ToolOutput omits falsy results and lacks trace copy/line-limit controls. */}
        {result && (
          <div className='p-3'>
            <CodeBlock
              code={code ?? ''}
              language='json'
              title={t('Tool result')}
              showToolbar
              maxExpandedLines={24}
            />
          </div>
        )}
      </ToolContent>
    </Tool>
  )
}

function TraceMessage(props: { entry: TraceConversationEntry }) {
  const { t } = useTranslation()
  const roles = {
    system: t('System'),
    developer: t('Developer'),
    user: t('User'),
    assistant: t('Assistant'),
    tool: t('Tool'),
    unknown: t('Unknown role'),
  }
  const message = props.entry.message
  const copyText = props.entry.parts
    .map(({ part }) => {
      if (part.type === 'text' || part.type === 'reasoning') return part.text
      if (part.type === 'tool-call' || part.type === 'tool-result') {
        return JSON.stringify(
          {
            id: part.id,
            name: part.name ?? part.result?.name,
            [part.type]: part.value,
            ...(part.result ? { 'tool-result': part.result.value } : {}),
            ...(part.isError || part.result?.isError ? { is_error: true } : {}),
          },
          null,
          2
        )
      }
      return JSON.stringify(part.value, null, 2)
    })
    .join('\n\n')
  // Tools already own a collapsed, bounded panel. A large matched result must
  // not hide the entire call card behind another message-level disclosure.
  const textLength = props.entry.parts.reduce((length, { part }) => {
    if (part.type === 'text' || part.type === 'reasoning') {
      return length + part.text.length
    }
    if (part.type === 'raw') return length + JSON.stringify(part.value).length
    return length
  }, 0)
  const isLongMessage = textLength > 8_000
  const [expanded, setExpanded] = useState(!isLongMessage)

  return (
    <Message
      from={message.role === 'user' ? 'user' : 'assistant'}
      role='article'
      aria-label={roles[message.role]}
    >
      <MessageContent
        variant='flat'
        className='w-full min-w-0 gap-3 p-3 group-[.is-user]:py-3'
      >
        <div className='flex items-center justify-between gap-2'>
          <span className='text-muted-foreground text-xs font-medium'>
            {roles[message.role]}
          </span>
          <CopyButton
            value={copyText}
            className='size-7'
            tooltip={t('Copy message')}
          />
        </div>
        <Collapsible
          open={expanded}
          onOpenChange={setExpanded}
          className='space-y-3'
        >
          {isLongMessage && (
            <CollapsibleTrigger className='text-muted-foreground group flex w-full items-center gap-2 text-xs'>
              <span>{expanded ? t('Collapse') : t('Expand')}</span>
              <ChevronDown
                className='size-3.5 group-data-[panel-open]:rotate-180'
                aria-hidden='true'
              />
            </CollapsibleTrigger>
          )}
          <CollapsibleContent className='max-h-[32rem] space-y-3 overflow-auto overscroll-contain'>
            {props.entry.parts.map((entry) => {
              const part = entry.part
              if (part.type === 'text') {
                return (
                  <Response key={entry.id} final>
                    {part.text}
                  </Response>
                )
              }
              if (part.type === 'reasoning') {
                return (
                  <Reasoning
                    key={entry.id}
                    isStreaming={false}
                    defaultOpen={false}
                  >
                    <ReasoningTrigger className='group inline-flex whitespace-nowrap'>
                      {t('Reasoning')}
                      <ChevronDown
                        className='size-3.5 group-data-[panel-open]:rotate-180'
                        aria-hidden='true'
                      />
                    </ReasoningTrigger>
                    <ReasoningContent>{part.text}</ReasoningContent>
                  </Reasoning>
                )
              }
              if (part.type === 'raw') {
                const code = JSON.stringify(part.value, null, 2)
                return (
                  <CodeBlock
                    key={entry.id}
                    code={code}
                    language='json'
                    title={t('Other content')}
                    showToolbar
                    defaultCollapsed
                    collapsedLines={6}
                    maxExpandedLines={24}
                  />
                )
              }
              return <TraceToolPart key={entry.id} part={part} />
            })}
          </CollapsibleContent>
        </Collapsible>
      </MessageContent>
    </Message>
  )
}

export function RequestTraceConversation(props: { legs: RequestTraceLeg[] }) {
  const { t } = useTranslation()
  const sources = useMemo(
    () =>
      buildRequestTraceConversation(props.legs).map((source) => ({
        ...source,
        // The capture sequence and positions identify immutable messages and blocks.
        entries: source.messages.flatMap(
          (message, position): TraceConversationEntry[] => {
            const parts = message.parts
              .map((part, block) => ({
                id: `${source.leg.seq}:${position}:${block}`,
                part,
              }))
              .filter(({ part }) => part.type !== 'tool-result' || !part.linked)
            if (parts.length === 0) return []
            return [{ id: `${source.leg.seq}:${position}`, message, parts }]
          }
        ),
      })),
    [props.legs]
  )
  const sourceLabels = {
    client_request: t('Input history'),
    client_response: t('Final response'),
    upstream_request: t('Upstream input'),
    upstream_response: t('Upstream response'),
  }

  if (sources.length === 0) {
    return <EmptyState title={t('No conversation was captured.')} />
  }

  return (
    <div className='mx-auto w-full max-w-4xl space-y-6'>
      <p className='text-muted-foreground text-xs leading-relaxed'>
        {t(
          'Conversation history sent with this request and its final response.'
        )}
      </p>
      {sources.map((source) => (
        <section
          key={source.leg.seq}
          className='min-w-0 space-y-3'
          aria-label={sourceLabels[source.leg.direction]}
        >
          <h3 className='text-muted-foreground text-xs font-medium'>
            {sourceLabels[source.leg.direction]}
          </h3>
          {source.leg.direction.startsWith('upstream_') && (
            <Alert>
              <AlertDescription>
                {t(
                  'Client payload was not captured. Showing the final upstream attempt.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {source.referencedHistory && (
            <Alert>
              <AlertDescription>
                {t(
                  'History referenced by an ID or cache is not included in this capture.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {source.hasAlternatives && (
            <Alert>
              <AlertDescription>
                {t(
                  'Only the first response alternative is shown. The raw payload contains all alternatives.'
                )}
              </AlertDescription>
            </Alert>
          )}
          {source.leg.truncated && (
            <Alert>
              <AlertDescription>
                {t(
                  'This payload was truncated. The conversation may be incomplete.'
                )}
              </AlertDescription>
            </Alert>
          )}
          <div className='space-y-4'>
            {source.entries.map((entry) => (
              <TraceMessage key={entry.id} entry={entry} />
            ))}
          </div>
          {source.rawFallback && (
            <div className='space-y-2'>
              {source.messages.length === 0 && (
                <p className='text-muted-foreground text-sm'>
                  {t(
                    'This payload cannot be shown as a conversation. Inspect its raw content in Exchange details.'
                  )}
                </p>
              )}
              {source.leg.body && (
                <CodeBlock
                  code={source.leg.body}
                  language='text'
                  title={t('Raw payload')}
                  showToolbar
                  defaultCollapsed
                  collapsedLines={6}
                  maxExpandedLines={24}
                />
              )}
            </div>
          )}
        </section>
      ))}
    </div>
  )
}
