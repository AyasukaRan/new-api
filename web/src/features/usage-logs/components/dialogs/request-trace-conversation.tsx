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
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronDown, Wrench } from 'lucide-react'
import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Message, MessageContent } from '@/components/ai-elements/message'
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from '@/components/ai-elements/reasoning'
import { Tool, ToolContent } from '@/components/ai-elements/tool'
import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { JsonViewer } from '@/components/json-viewer'
import { isStructuredJson } from '@/components/json-viewer/json-source'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

import {
  buildRequestTraceConversation,
  type TraceConversationMessage,
  type TraceConversationPart,
  type TraceConversationSource,
} from '../../lib/request-trace-conversation'
import {
  getTraceJsonContent,
  isJsonFenceLanguage,
  requestTraceMarkdown,
} from '../../lib/request-trace-json'
import type { RequestTraceLeg } from '../../types'
import {
  MAX_TRACE_CONTENT_CHARACTERS,
  RequestTraceContent,
} from './request-trace-content'

interface TraceConversationEntry {
  id: string
  message: TraceConversationMessage
  parts: { id: string; part: TraceConversationPart }[]
  continuedAssistant: boolean
}

function TraceToolResult(props: { value: unknown }) {
  const { t } = useTranslation()
  const code =
    typeof props.value === 'string'
      ? props.value
      : JSON.stringify(props.value, null, 2)
  const renderResultAsContent = useMemo(() => {
    if (
      typeof props.value !== 'string' ||
      !code ||
      code.length > MAX_TRACE_CONTENT_CHARACTERS ||
      !/[{[`~]/.test(code) ||
      isStructuredJson(code)
    ) {
      return false
    }
    // Confirm actual JSON blocks before rendering Markdown. Ordinary stdout
    // and malformed JSON keep their original whitespace and literal markup.
    return requestTraceMarkdown.parse(code, {}).some((token) => {
      const language = token.info.trim().toLowerCase()
      if (
        token.type === 'code_block' ||
        (token.type === 'fence' &&
          (language === '' || language === 'text' || language === 'plaintext'))
      ) {
        return isStructuredJson(token.content)
      }
      if (token.type !== 'fence' || !isJsonFenceLanguage(language)) return false
      try {
        JSON.parse(token.content.trim())
        return true
      } catch {
        return false
      }
    })
  }, [code, props.value])

  if (code && code.length > MAX_TRACE_CONTENT_CHARACTERS) {
    return (
      <div className='p-3'>
        <RequestTraceContent content={code} title={t('Tool result')} />
      </div>
    )
  }

  return (
    <div className='p-3'>
      {renderResultAsContent ? (
        <>
          <div className='mb-2 flex items-center justify-between gap-2'>
            <h4 className='text-muted-foreground text-xs font-medium'>
              {t('Tool result')}
            </h4>
            <CopyButton
              value={code ?? ''}
              className='size-7'
              tooltip={t('Copy to clipboard')}
            />
          </div>
          <RequestTraceContent content={code ?? ''} />
        </>
      ) : (
        <JsonViewer code={code ?? ''} title={t('Tool result')} />
      )}
    </div>
  )
}

function TraceToolPart(props: {
  part: Extract<TraceConversationPart, { type: 'tool-call' | 'tool-result' }>
}) {
  const { t } = useTranslation()
  const isCall = props.part.type === 'tool-call'
  const label = isCall ? t('Tool call') : t('Tool result')
  const result = isCall ? props.part.result : props.part
  const toolName = props.part.name ?? props.part.result?.name

  return (
    <Tool className='mb-0'>
      {/* ToolHeader requires a live execution status; a trace records only what was sent. */}
      <CollapsibleTrigger
        aria-label={[label, toolName || t('Unnamed tool'), props.part.id]
          .filter(Boolean)
          .join(' ')}
        className='group flex w-full items-center justify-between gap-2 px-3 py-2 text-left'
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
            <span
              className='text-muted-foreground max-w-40 truncate font-mono text-xs'
              title={props.part.id}
            >
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
        {isCall && (
          <div className='p-3'>
            <RequestTraceContent
              format='json'
              content={
                props.part.rawArguments ??
                (typeof props.part.value === 'string'
                  ? props.part.value
                  : JSON.stringify(props.part.value, null, 2))
              }
              title={t('Parameters')}
            />
          </div>
        )}
        {/* ToolOutput omits falsy results and lacks trace copy/line-limit controls. */}
        {result && <TraceToolResult value={result.value} />}
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
  const copyText = useMemo(
    () =>
      props.entry.parts
        .map(({ part }) => {
          if (part.type === 'text' || part.type === 'reasoning') {
            return part.text
          }
          if (part.type === 'tool-call' || part.type === 'tool-result') {
            return JSON.stringify(
              {
                id: part.id,
                name: part.name ?? part.result?.name,
                [part.type]: part.value,
                ...(part.result ? { 'tool-result': part.result.value } : {}),
                ...(part.isError || part.result?.isError
                  ? { is_error: true }
                  : {}),
              },
              null,
              2
            )
          }
          return JSON.stringify(part.value, null, 2)
        })
        .join('\n\n'),
    [props.entry.parts]
  )
  // JSON and tools own bounded disclosures. Only long prose needs
  // another message-level collapse; a JSON reply should immediately show its tree.
  const textLength = useMemo(
    () =>
      props.entry.parts.reduce((length, { part }) => {
        if (part.type === 'reasoning') return length + part.text.length
        if (
          part.type === 'text' &&
          (part.text.length > MAX_TRACE_CONTENT_CHARACTERS ||
            getTraceJsonContent(part.text) === undefined)
        ) {
          return length + part.text.length
        }
        return length
      }, 0),
    [props.entry.parts]
  )
  const isLongMessage = textLength > 8_000
  const isInstruction =
    message.role === 'system' || message.role === 'developer'
  const [expanded, setExpanded] = useState(!isInstruction && !isLongMessage)
  const previewPart = props.entry.parts.find(
    ({ part }) => part.type === 'text' || part.type === 'reasoning'
  )?.part
  const preview =
    previewPart?.type === 'text' || previewPart?.type === 'reasoning'
      ? previewPart.text.slice(0, 200).replaceAll(/\s+/g, ' ').trim()
      : ''

  return (
    <Message
      from={message.role === 'user' ? 'user' : 'assistant'}
      role='article'
      aria-label={roles[message.role]}
    >
      <MessageContent
        variant='flat'
        className={cn(
          'relative w-full min-w-0 gap-2 px-3 py-2 group-[.is-user]:py-3',
          isInstruction && 'bg-muted/30 border',
          props.entry.continuedAssistant && 'pt-0 pr-11'
        )}
      >
        <Collapsible
          open={expanded}
          onOpenChange={setExpanded}
          className='min-w-0 space-y-2'
        >
          {props.entry.continuedAssistant ? (
            <CopyButton
              value={copyText}
              className='absolute top-0 right-3 size-7 opacity-70 group-focus-within:opacity-100 focus-visible:opacity-100 [@media(hover:hover)]:opacity-0 [@media(hover:hover)]:group-hover:opacity-100'
              tooltip={t('Copy message')}
            />
          ) : (
            <div className='flex min-w-0 items-center justify-between gap-2'>
              {isInstruction ? (
                <CollapsibleTrigger
                  aria-label={`${expanded ? t('Collapse') : t('Expand')} ${roles[message.role]}`}
                  className='text-muted-foreground group focus-visible:ring-ring flex min-w-0 flex-1 items-center gap-2 rounded-sm text-left text-xs outline-none focus-visible:ring-2'
                >
                  <ChevronDown
                    className='size-3.5 shrink-0 -rotate-90 group-data-[panel-open]:rotate-0'
                    aria-hidden='true'
                  />
                  <span className='shrink-0 font-medium'>
                    {roles[message.role]}
                  </span>
                  {!expanded && preview && (
                    <span className='truncate'>{preview}</span>
                  )}
                </CollapsibleTrigger>
              ) : (
                <span className='text-muted-foreground text-xs font-medium'>
                  {roles[message.role]}
                </span>
              )}
              <CopyButton
                value={copyText}
                className='size-7'
                tooltip={t('Copy message')}
              />
            </div>
          )}
          {isLongMessage && !isInstruction && (
            <CollapsibleTrigger className='text-muted-foreground group flex w-full items-center gap-2 text-xs'>
              <span>{expanded ? t('Collapse') : t('Expand')}</span>
              <ChevronDown
                className='size-3.5 group-data-[panel-open]:rotate-180'
                aria-hidden='true'
              />
            </CollapsibleTrigger>
          )}
          {!expanded && !isInstruction && preview && (
            <p className='text-muted-foreground line-clamp-2 text-sm'>
              {preview}
            </p>
          )}
          <CollapsibleContent
            className={cn(
              'min-w-0 space-y-2',
              isLongMessage && 'max-h-[32rem] overflow-auto overscroll-contain'
            )}
          >
            {props.entry.parts.map((entry) => {
              const part = entry.part
              if (part.type === 'text') {
                return (
                  <RequestTraceContent key={entry.id} content={part.text} />
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
                    <ReasoningContent>
                      <RequestTraceContent content={part.text} />
                    </ReasoningContent>
                  </Reasoning>
                )
              }
              if (part.type === 'raw') {
                const code = JSON.stringify(part.value, null, 2)
                return (
                  <RequestTraceContent
                    key={entry.id}
                    format='json'
                    content={code}
                    title={t('Other content')}
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

type TraceConversationSourceEntries = TraceConversationSource & {
  entries: TraceConversationEntry[]
}

function TraceSourceHeader(props: { source: TraceConversationSource }) {
  const { t } = useTranslation()
  const sourceLabels = {
    client_request: t('Input history'),
    client_response: t('Final response'),
    upstream_request: t('Upstream input'),
    upstream_response: t('Upstream response'),
  }
  return (
    <div className='space-y-3'>
      <h3 className='text-muted-foreground text-xs font-medium'>
        {sourceLabels[props.source.leg.direction]}
      </h3>
      {props.source.leg.direction.startsWith('upstream_') && (
        <Alert>
          <AlertDescription>
            {t(
              'Client payload was not captured. Showing the final upstream attempt.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {props.source.referencedHistory && (
        <Alert>
          <AlertDescription>
            {t(
              'History referenced by an ID or cache is not included in this capture.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {props.source.hasAlternatives && (
        <Alert>
          <AlertDescription>
            {t(
              'Only the first response alternative is shown. The raw payload contains all alternatives.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {props.source.leg.truncated && (
        <Alert>
          <AlertDescription>
            {t(
              'This payload was truncated. The conversation may be incomplete.'
            )}
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}

function TraceSourceFallback(props: { source: TraceConversationSource }) {
  const { t } = useTranslation()
  if (!props.source.rawFallback) return null
  return (
    <div className='space-y-2'>
      {props.source.messages.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'This payload cannot be shown as a conversation. Inspect its raw content in Exchange details.'
          )}
        </p>
      )}
      {props.source.leg.body &&
        (props.source.leg.body.length > MAX_TRACE_CONTENT_CHARACTERS ? (
          <RequestTraceContent
            content={props.source.leg.body}
            title={t('Raw payload')}
          />
        ) : (
          <JsonViewer code={props.source.leg.body} title={t('Raw payload')} />
        ))}
    </div>
  )
}

type ConversationRow =
  | {
      id: string
      type: 'header' | 'fallback'
      source: TraceConversationSourceEntries
    }
  | { id: string; type: 'message'; entry: TraceConversationEntry }

function WindowedTraceConversation(props: {
  sources: TraceConversationSourceEntries[]
}) {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const anchorRef = useRef<{ key: string; offset: number } | null>(null)
  const rows = useMemo(
    () =>
      props.sources.flatMap((source): ConversationRow[] => [
        { id: `header:${source.leg.seq}`, type: 'header', source },
        ...source.entries.map(
          (entry): ConversationRow => ({ id: entry.id, type: 'message', entry })
        ),
        ...(source.rawFallback
          ? [
              {
                id: `fallback:${source.leg.seq}`,
                type: 'fallback' as const,
                source,
              },
            ]
          : []),
      ]),
    [props.sources]
  )
  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    getItemKey: (index) => rows[index].id,
    estimateSize: () => 180,
    overscan: 3,
    useFlushSync: false,
    initialRect: { width: 900, height: 600 },
  })
  // Keep the visible semantic row when delayed input history is inserted before
  // a final response that the administrator has already started reading.
  useLayoutEffect(() => {
    const anchor = anchorRef.current
    if (anchor) {
      const index = rows.findIndex((row) => row.id === anchor.key)
      const target =
        index < 0 ? undefined : virtualizer.getOffsetForIndex(index, 'start')
      if (target) virtualizer.scrollToOffset(target[0] + anchor.offset)
    } else if (rows[0]) {
      anchorRef.current = { key: rows[0].id, offset: 0 }
    }
  }, [rows, virtualizer])
  const finalIndex = rows.findIndex(
    (row) =>
      row.type === 'header' && row.source.leg.direction.endsWith('response')
  )

  return (
    <div className='mx-auto flex h-full min-h-0 w-full max-w-4xl flex-col gap-3'>
      <div className='flex shrink-0 items-center justify-between gap-3'>
        <p className='text-muted-foreground text-xs leading-relaxed'>
          {t(
            'Conversation history sent with this request and its final response.'
          )}
        </p>
        {finalIndex >= 0 && (
          <Button
            size='sm'
            variant='ghost'
            onClick={() => {
              anchorRef.current = { key: rows[finalIndex].id, offset: 0 }
              virtualizer.scrollToIndex(finalIndex, { align: 'start' })
            }}
          >
            {t('Back to final response')}
          </Button>
        )}
      </div>
      <div
        ref={scrollRef}
        role='log'
        aria-label={t('Conversation')}
        tabIndex={0}
        className='min-h-0 flex-1 overflow-y-auto overscroll-contain'
        onScroll={() => {
          const offset = scrollRef.current?.scrollTop ?? 0
          const visible = virtualizer
            .getVirtualItems()
            .find((item) => item.end > offset)
          if (visible) {
            anchorRef.current = {
              key: String(visible.key),
              offset: offset - visible.start,
            }
          }
        }}
      >
        <div
          className='relative w-full'
          style={{ height: virtualizer.getTotalSize() }}
        >
          {virtualizer.getVirtualItems().map((item) => {
            const row = rows[item.index]
            return (
              <div
                key={row.id}
                data-index={item.index}
                ref={virtualizer.measureElement}
                className={cn(
                  'absolute top-0 left-0 w-full',
                  row.type === 'message' ? 'pb-2' : 'pb-3'
                )}
                style={{ transform: `translateY(${item.start}px)` }}
              >
                {row.type === 'message' ? (
                  <TraceMessage entry={row.entry} />
                ) : null}
                {row.type === 'header' ? (
                  <TraceSourceHeader source={row.source} />
                ) : null}
                {row.type === 'fallback' ? (
                  <TraceSourceFallback source={row.source} />
                ) : null}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

export function RequestTraceConversation(props: {
  legs: RequestTraceLeg[]
  sources?: TraceConversationSource[]
}) {
  const { t } = useTranslation()
  const sources = useMemo(
    () =>
      (props.sources ?? buildRequestTraceConversation(props.legs)).map(
        (source) => {
          const occurrences = new Map<string, number>()
          const entries = source.messages.flatMap(
            (message): TraceConversationEntry[] => {
              // Late top-level instructions can be inserted before streamed history.
              // Identify messages by bounded content samples, never their changing index.
              const signature = JSON.stringify([
                message.role,
                ...message.parts.map((part) => {
                  if (part.type === 'text' || part.type === 'reasoning') {
                    return [
                      part.type,
                      part.text.length,
                      part.text.slice(0, 80),
                      part.text.slice(-80),
                    ]
                  }
                  if (
                    part.type === 'tool-call' ||
                    part.type === 'tool-result'
                  ) {
                    return [part.type, part.id, part.name]
                  }
                  return [
                    part.type,
                    typeof part.value,
                    String(part.value).slice(0, 80),
                  ]
                }),
              ])
              const occurrence = occurrences.get(signature) ?? 0
              occurrences.set(signature, occurrence + 1)
              const id = `${source.leg.seq}:${signature}:${occurrence}`
              const parts = message.parts
                .map((part, block) => ({ id: `${id}:${block}`, part }))
                .filter(
                  ({ part }) => part.type !== 'tool-result' || !part.linked
                )
              return parts.length === 0
                ? []
                : [{ id, message, parts, continuedAssistant: false }]
            }
          )
          for (const [index, entry] of entries.entries()) {
            entry.continuedAssistant =
              entry.message.role === 'assistant' &&
              entries[index - 1]?.message.role === 'assistant'
          }
          return { ...source, entries }
        }
      ),
    [props.legs, props.sources]
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

  if (
    props.sources ||
    sources.reduce((count, source) => count + source.entries.length, 0) > 60
  ) {
    return <WindowedTraceConversation sources={sources} />
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
          <TraceSourceHeader source={source} />
          <div className='space-y-2'>
            {source.entries.map((entry) => (
              <TraceMessage key={entry.id} entry={entry} />
            ))}
          </div>
          <TraceSourceFallback source={source} />
        </section>
      ))}
    </div>
  )
}
