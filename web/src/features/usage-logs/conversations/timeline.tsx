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
import { Fragment, useMemo, useState } from 'react'
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
  const forks = new Map<number, ThreadLane[]>()
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
      const atRow = forks.get(thread.forkRow) ?? []
      atRow.push(thread)
      forks.set(thread.forkRow, atRow)
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
    forks,
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
  // Use the same scale for every row, including expanded ones. Narrow screens
  // can still read names and timestamps when several agents run concurrently.
  const position = (lane: number) => `${((lane + 0.5) / graph.lanes) * 100}%`
  const label = (thread: ThreadLane) =>
    thread.name || (thread.parent ? t('Subagent') : t('Main thread'))
  return (
    <div
      role='list'
      aria-label={t('Conversation timeline')}
      className='isolate grid min-w-0 content-start px-1'
      style={{
        gridTemplateColumns: `min(30%, ${graph.lanes * 14 + 6}px) minmax(0, 1fr)`,
      }}
    >
      {Array.from(
        graph.threads.values(),
        (thread) =>
          thread.firstRow < thread.lastRow && (
            <div
              key={thread.id}
              aria-hidden='true'
              className='pointer-events-none relative col-start-1 translate-y-5'
              style={{
                gridRow: `${thread.firstRow + 1} / ${thread.lastRow + 1}`,
              }}
            >
              <span
                className={cn(
                  'absolute inset-y-0 border-l-2 border-current opacity-65',
                  laneColors[thread.lane % laneColors.length]
                )}
                style={{ left: position(thread.lane) }}
              />
            </div>
          )
      )}
      {graph.ordered.map((turn, index) => {
        const thread = graph.threads.get(turn.conversation_id)
        if (!thread) return null
        const parent = graph.threads.get(thread.parent)
        const color = laneColors[thread.lane % laneColors.length]
        const open = expanded === turn.trace_id
        const time = formatTimestampToDate(start(turn), 'milliseconds')
        return (
          <Fragment key={turn.trace_id}>
            <div
              aria-hidden='true'
              className={cn('pointer-events-none relative col-start-1', color)}
              style={{ gridRow: index + 1 }}
            >
              {graph.forks.get(index)?.map((child) => {
                const origin = graph.threads.get(child.parent)
                if (!origin) return null
                const left = Math.min(child.lane, origin.lane)
                const right = Math.max(child.lane, origin.lane)
                const border =
                  origin.lane < child.lane
                    ? 'rounded-tl-lg border-l-2'
                    : 'rounded-tr-lg border-r-2'
                const join =
                  origin.lane < child.lane
                    ? 'rounded-br-lg border-r-2'
                    : 'rounded-bl-lg border-l-2'
                return (
                  <span
                    key={child.id}
                    className={cn(
                      'absolute h-3 border-current',
                      laneColors[child.lane % laneColors.length],
                      child.anchor ? 'top-2 border-b-2' : 'top-5 border-t-2',
                      child.anchor ? join : border
                    )}
                    style={{
                      left: position(left),
                      right: position(graph.lanes - 1 - right),
                    }}
                  />
                )
              })}
              <span
                className={cn(
                  'absolute top-5 z-10 size-2.5 translate-x-[calc(-50%+1px)] -translate-y-1/2 rounded-full border-2 border-current',
                  open ? 'bg-current' : 'bg-background'
                )}
                style={{ left: position(thread.lane) }}
              />
            </div>
            <div
              role='listitem'
              className='col-start-2 min-w-0 pl-2'
              style={{ gridRow: index + 1 }}
            >
              <Button
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
          </Fragment>
        )
      })}
    </div>
  )
}
