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
import { Grip, Maximize2, Settings2, Trash2 } from 'lucide-react'
import { useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useMediaQuery } from '@/hooks/use-media-query'
import { cn } from '@/lib/utils'

import { placeDashboardPanel } from './layout'
import { DashboardPanelView } from './panel'
import { METRIC_LABELS } from './registry'
import type { BoardTime, DashboardPanel } from './types'

const ROW_HEIGHT = 56
const GAP = 12

export function DashboardGrid(props: {
  panels: DashboardPanel[]
  time: BoardTime
  editing: boolean
  isAdmin: boolean
  userId: number
  onChange: (panels: DashboardPanel[]) => void
  onConfigure: (panel: DashboardPanel) => void
  onRemove: (panel: DashboardPanel) => void
}) {
  const { t } = useTranslation()
  const mobile = useMediaQuery('(max-width: 767px)')
  const grid = useRef<HTMLDivElement>(null)
  const gesture = useRef<{
    id: string
    mode: 'move' | 'resize'
    pointer: number
    x: number
    y: number
    width: number
    panels: DashboardPanel[]
    layout: DashboardPanel['layout']
  } | null>(null)
  const previewRef = useRef<DashboardPanel[] | null>(null)
  const [preview, setPreview] = useState<DashboardPanel[] | null>(null)
  const panels = [...(preview ?? props.panels)].sort(
    (a, b) => a.layout.y - b.layout.y || a.layout.x - b.layout.x
  )
  const start = (
    event: PointerEvent<HTMLButtonElement>,
    panel: DashboardPanel,
    mode: 'move' | 'resize'
  ) => {
    if (!props.editing || event.button !== 0 || mobile) return
    event.preventDefault()
    event.currentTarget.focus({ preventScroll: true })
    gesture.current = {
      id: panel.id,
      mode,
      pointer: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      width: grid.current?.getBoundingClientRect().width ?? 1200,
      panels: props.panels,
      layout: panel.layout,
    }
    event.currentTarget.setPointerCapture?.(event.pointerId)
  }
  const move = (event: PointerEvent<HTMLButtonElement>) => {
    const current = gesture.current
    if (!current || current.pointer !== event.pointerId) return
    const dx = Math.round(
      (event.clientX - current.x) / ((current.width + GAP) / 12)
    )
    const dy = Math.round((event.clientY - current.y) / (ROW_HEIGHT + GAP))
    const layout =
      current.mode === 'move'
        ? {
            ...current.layout,
            x: current.layout.x + dx,
            y: current.layout.y + dy,
          }
        : {
            ...current.layout,
            w: current.layout.w + dx,
            h: current.layout.h + dy,
          }
    previewRef.current = placeDashboardPanel(current.panels, current.id, layout)
    setPreview(previewRef.current)
  }
  const stop = (commit: boolean) => {
    if (commit && previewRef.current) props.onChange(previewRef.current)
    previewRef.current = null
    gesture.current = null
    setPreview(null)
  }
  const key = (
    event: KeyboardEvent<HTMLButtonElement>,
    panel: DashboardPanel,
    resize: boolean
  ) => {
    if (event.key === 'Escape') {
      stop(false)
      return
    }
    const delta = {
      ArrowLeft: [-1, 0],
      ArrowRight: [1, 0],
      ArrowUp: [0, -1],
      ArrowDown: [0, 1],
    }[event.key]
    if (!delta) return
    event.preventDefault()
    if (mobile && !resize) {
      const index = panels.findIndex((item) => item.id === panel.id)
      const other = panels[index + (delta[1] || delta[0])]
      if (!other) return
      const ordered = [...panels]
      ordered[index] = other
      ordered[index + (delta[1] || delta[0])] = panel
      let y = 0
      props.onChange(
        ordered.map((item) => {
          const result = { ...item, layout: { ...item.layout, x: 0, y } }
          y += item.layout.h
          return result
        })
      )
      return
    }
    props.onChange(
      placeDashboardPanel(
        props.panels,
        panel.id,
        resize
          ? {
              ...panel.layout,
              w: panel.layout.w + delta[0],
              h: panel.layout.h + delta[1],
            }
          : {
              ...panel.layout,
              x: panel.layout.x + delta[0],
              y: panel.layout.y + delta[1],
            }
      )
    )
  }
  return (
    <div
      ref={grid}
      className='grid min-w-0 gap-3'
      style={{
        gridTemplateColumns: mobile
          ? 'minmax(0, 1fr)'
          : 'repeat(12, minmax(0, 1fr))',
        gridAutoRows: mobile ? undefined : `${ROW_HEIGHT}px`,
      }}
      aria-label={t('Dashboard panels')}
    >
      {panels.map((panel) => (
        <section
          key={panel.id}
          aria-label={panel.title || t(METRIC_LABELS[panel.metric])}
          className={cn(
            'bg-card relative flex min-w-0 flex-col overflow-hidden rounded-2xl border shadow-xs',
            props.editing && 'border-dashed'
          )}
          style={
            mobile
              ? { height: panel.layout.h * (ROW_HEIGHT + GAP) - GAP }
              : {
                  gridColumn: `${panel.layout.x + 1} / span ${panel.layout.w}`,
                  gridRow: `${panel.layout.y + 1} / span ${panel.layout.h}`,
                }
          }
        >
          <header className='flex min-w-0 shrink-0 items-center gap-1 border-b px-3 py-2'>
            {props.editing && (
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                className='cursor-grab touch-none'
                aria-label={t('Move panel {{name}}', {
                  name: panel.title || t(METRIC_LABELS[panel.metric]),
                })}
                title={t('Drag to move. Arrow keys also move the panel.')}
                onPointerDown={(event) => start(event, panel, 'move')}
                onPointerMove={move}
                onPointerUp={(event) => {
                  move(event)
                  stop(true)
                }}
                onPointerCancel={() => stop(false)}
                onLostPointerCapture={() => stop(false)}
                onKeyDown={(event) => key(event, panel, false)}
              >
                <Grip />
              </Button>
            )}
            <h3
              className='min-w-0 flex-1 truncate text-sm font-semibold'
              title={panel.title}
            >
              {panel.title || t(METRIC_LABELS[panel.metric])}
            </h3>
            {panel.time && (
              <span className='text-muted-foreground shrink-0 text-xs'>
                {panel.time.range}
              </span>
            )}
            {props.editing && (
              <>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  aria-label={t('Configure panel {{name}}', {
                    name: panel.title || t(METRIC_LABELS[panel.metric]),
                  })}
                  onClick={() => props.onConfigure(panel)}
                >
                  <Settings2 />
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='icon-sm'
                  aria-label={t('Remove panel {{name}}', {
                    name: panel.title || t(METRIC_LABELS[panel.metric]),
                  })}
                  onClick={() => props.onRemove(panel)}
                >
                  <Trash2 />
                </Button>
              </>
            )}
          </header>
          <div className='flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden'>
            <DashboardPanelView
              panel={panel}
              time={panel.time ?? props.time}
              isAdmin={props.isAdmin}
              userId={props.userId}
            />
          </div>
          {props.editing && (
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              className='bg-card/90 absolute right-0 bottom-0 cursor-se-resize touch-none'
              aria-label={t('Resize panel {{name}}', {
                name: panel.title || t(METRIC_LABELS[panel.metric]),
              })}
              title={t('Drag to resize. Arrow keys also resize the panel.')}
              onPointerDown={(event) => start(event, panel, 'resize')}
              onPointerMove={move}
              onPointerUp={(event) => {
                move(event)
                stop(true)
              }}
              onPointerCancel={() => stop(false)}
              onLostPointerCapture={() => stop(false)}
              onKeyDown={(event) => key(event, panel, true)}
            >
              <Maximize2 className='size-3' />
            </Button>
          )}
        </section>
      ))}
    </div>
  )
}
