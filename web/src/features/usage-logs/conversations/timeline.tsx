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
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { RecordedTurn } from './api'
import { RecordedTurnBody } from './recorded-turn-body'

const laneColors = [
  'text-sky-600 dark:text-sky-400',
  'text-violet-600 dark:text-violet-400',
  'text-emerald-600 dark:text-emerald-400',
  'text-amber-600 dark:text-amber-400',
  'text-pink-600 dark:text-pink-400',
]

interface ThreadLane {
  id: string
  parent: string
  name: string
  lane: number
  firstRow: number
  lastRow: number
  forkRow: number
  parentTurn: string
  anchor?: RecordedTurn
}

function start(turn: RecordedTurn) {
  return turn.started_at_ms || turn.created_at * 1000
}

// Row zero is the newest event. Extend a parent's rail to its recorded forks,
// then reuse a lane once both the thread and all its forks are out of it.
function layoutTimeline(turns: RecordedTurn[], root: string) {
  const ordered = [...turns].sort(
    (a, b) => start(b) - start(a) || b.trace_id.localeCompare(a.trace_id)
  )
  const threads = new Map<string, ThreadLane>()
  const previousTurns = new Map<string, RecordedTurn>()
  const lastSeen = new Map<string, RecordedTurn>()
  const turnRows = new Map<string, number[]>()
  for (let row = ordered.length - 1; row >= 0; row--) {
    const turn = ordered[row]
    const id = turn.conversation_id
    if (turn.turn_id) {
      const rows = turnRows.get(turn.turn_id) ?? []
      rows.push(row)
      turnRows.set(turn.turn_id, rows)
    }
    const previous = lastSeen.get(id)
    if (previous) previousTurns.set(turn.trace_id, previous)
    lastSeen.set(id, turn)
    let thread = threads.get(id)
    if (!thread) {
      thread = {
        id,
        parent: '',
        name: '',
        lane: 0,
        firstRow: row,
        lastRow: row,
        forkRow: row,
        parentTurn: turn.parent_turn_id || '',
      }
      threads.set(id, thread)
    }
    thread.firstRow = row
    thread.name ||= turn.agent_name
    thread.parentTurn ||= turn.parent_turn_id || ''
    if (turn.parent_id !== id) thread.parent ||= turn.parent_id
  }
  for (const rows of turnRows.values()) rows.reverse()
  for (const thread of threads.values()) {
    const parent = threads.get(thread.parent)
    if (parent) {
      // One client turn can issue several requests. Anchor to the closest
      // recorded parent request in that exact turn at or before the fork.
      const anchorRow = turnRows
        .get(thread.parentTurn)
        ?.find(
          (i) => i >= thread.forkRow && ordered[i].conversation_id === parent.id
        )
      if (anchorRow !== undefined) {
        thread.anchor = ordered[anchorRow]
        thread.forkRow = anchorRow
        thread.lastRow = Math.max(thread.lastRow, anchorRow)
      }
      parent.firstRow = Math.min(parent.firstRow, thread.forkRow)
      parent.lastRow = Math.max(parent.lastRow, thread.forkRow)
    }
  }
  const laneStartRows: number[] = []
  const branches = [...threads.values()].filter((t) => t.id !== root)
  for (const branch of branches.sort((a, b) => b.lastRow - a.lastRow)) {
    let index = laneStartRows.findIndex((row) => row > branch.lastRow)
    if (index < 0) index = laneStartRows.length
    branch.lane = index + 1
    laneStartRows[index] = branch.firstRow
  }
  return {
    ordered,
    threads,
    previousTurns,
    lanes: laneStartRows.length + 1,
  }
}

export function ConversationTimeline(props: {
  turns: RecordedTurn[]
  conversationId: string
}) {
  const { t } = useTranslation()
  const graph = useMemo(
    () => layoutTimeline(props.turns, props.conversationId),
    [props.turns, props.conversationId]
  )
  const [expanded, setExpanded] = useState<string | null>(
    () => graph.ordered[0]?.trace_id ?? null
  )
  const list = useRef<HTMLDivElement>(null)
  const canvas = useRef<SVGSVGElement>(null)
  const [layout, setLayout] = useState<{ width: number; centers: number[] }>({
    width: 0,
    centers: [],
  })
  useLayoutEffect(() => {
    const container = list.current
    const svg = canvas.current
    if (!container || !svg) return
    const headers = [
      ...container.querySelectorAll<HTMLElement>('[data-timeline-event]'),
    ]
    const measure = () => {
      const rect = svg.getBoundingClientRect()
      const centers = headers.map((header) => {
        const { top, height } = header.getBoundingClientRect()
        return top + height / 2 - rect.top
      })
      setLayout((previous) =>
        previous.width === rect.width &&
        previous.centers.length === centers.length &&
        centers.every((y, i) => y === previous.centers[i])
          ? previous
          : { width: rect.width, centers }
      )
    }
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(measure)
    observer.observe(svg)
    for (const header of headers) {
      observer.observe(header)
      if (header.parentElement) observer.observe(header.parentElement)
    }
    return () => observer.disconnect()
  }, [graph, expanded])

  // The SVG and timeline occupy the same grid rows. All paths and nodes use
  // the measured center of the event header, even after an image or tool opens.
  const x = (lane: number) => ((lane + 0.5) / graph.lanes) * layout.width
  const center = (row: number) => layout.centers[row]
  const joinY = (thread: ThreadLane) =>
    center(thread.forkRow) + (thread.anchor ? 0 : 12)
  const bottoms = new Map<string, number>()
  for (const thread of graph.threads.values()) {
    if (graph.threads.has(thread.parent)) {
      bottoms.set(
        thread.parent,
        Math.max(bottoms.get(thread.parent) ?? 0, joinY(thread))
      )
    }
  }
  const label = (thread: ThreadLane) =>
    thread.name || (thread.parent ? t('Subagent') : t('Main thread'))
  return (
    <div
      ref={list}
      role='list'
      aria-label={t('Conversation timeline')}
      className='relative isolate grid min-w-0 content-start px-1'
      style={{
        gridTemplateColumns: `min(30%, ${graph.lanes * 14 + 6}px) minmax(0, 1fr)`,
      }}
    >
      <svg
        ref={canvas}
        aria-hidden='true'
        data-timeline-graph=''
        className='pointer-events-none absolute inset-0 col-start-1 col-end-2 h-full w-full overflow-visible'
        style={{ gridRow: `1 / span ${Math.max(1, graph.ordered.length)}` }}
        fill='none'
        strokeWidth='2'
        strokeLinecap='round'
      >
        {layout.width > 0 && layout.centers.length === graph.ordered.length && (
          <>
            {Array.from(graph.threads.values(), (thread) => {
              const parent = graph.threads.get(thread.parent)
              const lane = x(thread.lane)
              const top = center(thread.firstRow)
              const bottom = Math.max(
                center(thread.lastRow),
                bottoms.get(thread.id) ?? 0
              )
              let path = `M ${lane} ${top} V ${bottom}`
              if (parent) {
                const parentX = x(parent.lane)
                const end = joinY(thread)
                const bend = Math.min(thread.anchor ? 18 : 12, end - top)
                path = `M ${lane} ${top} V ${end - bend} C ${lane} ${end - bend / 2} ${parentX} ${end - bend / 2} ${parentX} ${end}`
              }
              return (
                <path
                  key={thread.id}
                  data-thread={thread.id}
                  d={path}
                  className={laneColors[thread.lane % laneColors.length]}
                  stroke='currentColor'
                />
              )
            })}
            {graph.ordered.map((turn, row) => {
              const thread = graph.threads.get(turn.conversation_id)
              if (!thread) return null
              return (
                <circle
                  key={turn.trace_id}
                  data-event={turn.trace_id}
                  cx={x(thread.lane)}
                  cy={center(row)}
                  r='4'
                  stroke='currentColor'
                  className={cn(
                    laneColors[thread.lane % laneColors.length],
                    turn.trace_id === expanded
                      ? 'fill-current'
                      : 'fill-background'
                  )}
                />
              )
            })}
          </>
        )}
      </svg>
      {graph.ordered.map((turn, index) => {
        const thread = graph.threads.get(turn.conversation_id)
        if (!thread) return null
        const parent = graph.threads.get(thread.parent)
        const color = laneColors[thread.lane % laneColors.length]
        const open = expanded === turn.trace_id
        const time = formatTimestampToDate(start(turn), 'milliseconds')
        return (
          <div
            key={turn.trace_id}
            role='listitem'
            className='col-start-2 min-w-0 pl-2'
            style={{ gridRow: index + 1 }}
          >
            <Button
              data-timeline-event=''
              id={`event-${turn.trace_id}`}
              type='button'
              variant='ghost'
              aria-expanded={open}
              aria-controls={open ? `turn-${turn.trace_id}` : undefined}
              className='h-auto min-h-10 w-full justify-start gap-2 rounded-md px-2 py-1.5 text-left'
              onClick={() => setExpanded(open ? null : turn.trace_id)}
            >
              <span className={cn('truncate text-sm font-semibold', color)}>
                {label(thread)}
              </span>
              {thread.parent && (
                <span className='sr-only'>
                  {t('Parent thread: {{name}}', {
                    name: parent ? label(parent) : thread.parent,
                  })}
                </span>
              )}
              {turn.model_name && (
                <span className='text-muted-foreground truncate text-xs'>
                  {turn.model_name}
                </span>
              )}
              <time
                dateTime={new Date(start(turn)).toISOString()}
                title={time}
                className='text-muted-foreground ms-auto shrink-0 text-xs tabular-nums'
              >
                {time}
              </time>
              {open ? (
                <ChevronDown aria-hidden='true' />
              ) : (
                <ChevronRight aria-hidden='true' />
              )}
            </Button>
            {open && (
              <div className='pb-4' id={`turn-${turn.trace_id}`}>
                {thread.anchor && parent && (
                  <Button
                    type='button'
                    variant='link'
                    size='sm'
                    className='text-muted-foreground'
                    onClick={() => {
                      const anchor = thread.anchor
                      if (!anchor) return
                      setExpanded(anchor.trace_id)
                      document
                        .getElementById(`event-${anchor.trace_id}`)
                        ?.focus()
                    }}
                  >
                    {t('Parent thread: {{name}}', { name: label(parent) })}
                  </Button>
                )}
                <RecordedTurnBody
                  turn={turn}
                  previousTurn={graph.previousTurns.get(turn.trace_id)}
                />
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
