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
import { getRouteApi } from '@tanstack/react-router'
import { ListFilter } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

import { LOG_TYPE_ALL_VALUE } from '../constants'
import type { UsageLog } from '../data/schema'
import {
  getLogColumnFilterKeys,
  getLogTypeValue,
  isValidLogChannelFilter,
  type LogColumnFilterKey,
  type LogColumnFilterValues,
  type LogFilterColumn,
} from '../lib/filter'
import { getLogMetadata } from '../lib/format'
import { LogColumnFilterFields } from './log-column-filter-fields'
import { useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

const suggestionsFromLog: Partial<
  Record<LogColumnFilterKey, (log: UsageLog) => string | undefined>
> = {
  clientTool: (log) => getLogMetadata(log)?.client_tool,
  reasoningEffort: (log) => getLogMetadata(log)?.reasoning_effort,
  model: (log) => log.model_name,
  requestPath: (log) => getLogMetadata(log)?.request_path?.split('?', 1)[0],
  channel: (log) => (log.channel ? String(log.channel) : undefined),
}

export function LogHeaderFilter(props: {
  column: LogFilterColumn
  title: string
  isAdmin: boolean
  withReasoning: boolean
  logs: UsageLog[]
}) {
  const { t } = useTranslation()
  const { sensitiveVisible } = useUsageLogsContext()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<LogColumnFilterValues>({})
  const [suggestions, setSuggestions] = useState<
    Partial<Record<LogColumnFilterKey, string[]>>
  >({})
  const columns: LogFilterColumn[] = props.withReasoning
    ? [props.column, 'reasoning']
    : [props.column]
  const keys = columns.flatMap((column) =>
    getLogColumnFilterKeys(column, props.isAdmin)
  )
  const selected: LogColumnFilterValues = Object.fromEntries(
    keys.map((key) => {
      if (key !== 'type') return [key, search[key] ?? '']
      const type = getLogTypeValue(search.type)
      return [key, type === LOG_TYPE_ALL_VALUE ? '' : type]
    })
  )
  const active = keys.filter((key) => !!selected[key]).length
  const label = `${t('Filter')} · ${props.title}`

  function handleOpen(nextOpen: boolean) {
    if (nextOpen) {
      setDraft(selected)
      // These are suggestions only. A typed value searches the entire selected period.
      const options: Partial<Record<LogColumnFilterKey, string[]>> = {}
      for (const key of keys) {
        const getValue = suggestionsFromLog[key]
        if (!getValue) continue
        const values = new Set<string>()
        for (const log of props.logs) {
          const value = getValue(log)
          if (typeof value === 'string' && value.trim()) {
            values.add(value.trim())
          }
        }
        options[key] = [...values]
      }
      setSuggestions(options)
    }
    setOpen(nextOpen)
  }

  function apply(values: LogColumnFilterValues) {
    if (!isValidLogChannelFilter(values.channel)) return
    void navigate({
      search: (previous) => ({
        ...previous,
        ...Object.fromEntries(
          keys.map((key) => {
            const value = values[key]?.trim()
            if (key === 'type') {
              return [
                key,
                value && value !== LOG_TYPE_ALL_VALUE ? [value] : undefined,
              ]
            }
            return [key, value || undefined]
          })
        ),
        page: 1,
      }),
      resetScroll: false,
    })
    setOpen(false)
  }

  return (
    <Popover open={open} onOpenChange={handleOpen}>
      <PopoverTrigger
        render={
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={active ? `${label} (${active})` : label}
            title={label}
            className={cn(
              'text-muted-foreground/80 hover:text-foreground shrink-0',
              active > 0 && 'bg-primary/10 text-primary hover:text-primary'
            )}
          />
        }
      >
        <ListFilter aria-hidden='true' />
      </PopoverTrigger>
      <PopoverContent
        align='start'
        className='max-h-(--available-height) w-64 gap-3 overflow-y-auto overscroll-contain p-3'
      >
        <PopoverTitle>{props.title}</PopoverTitle>
        <form
          className='grid gap-2'
          onSubmit={(event) => {
            event.preventDefault()
            apply(draft)
          }}
        >
          {columns.map((column) => (
            <LogColumnFilterFields
              inlineSuggestions
              key={column}
              column={column}
              isAdmin={props.isAdmin}
              values={draft}
              suggestions={suggestions}
              maskClass={
                sensitiveVisible ? undefined : '[-webkit-text-security:disc]'
              }
              onChange={(key, value) =>
                setDraft((prev) => ({ ...prev, [key]: value }))
              }
            />
          ))}
          <div className='mt-1 flex items-center justify-end gap-2'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              disabled={!active && !keys.some((key) => draft[key])}
              onClick={() => apply({})}
            >
              {t('Clear')}
            </Button>
            <Button
              type='submit'
              size='sm'
              disabled={!isValidLogChannelFilter(draft.channel)}
            >
              {t('Apply')}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  )
}
