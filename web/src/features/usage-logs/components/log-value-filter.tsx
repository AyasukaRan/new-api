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
/* eslint-disable react-refresh/only-export-components */
import { X } from 'lucide-react'
import {
  createContext,
  useContext,
  useMemo,
  type ReactNode,
  type MouseEventHandler,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

import {
  getLogValueFilterKeys,
  getLogFilterValues,
  logColumnFilterLabels,
  type LogColumnFilterKey,
} from '../lib/filter'
import type { LogFilterValue } from '../types'
import { useUsageLogsContext } from './usage-logs-provider'

export type LogValueFilterKey = Exclude<LogColumnFilterKey, 'type'>

interface LogValueFilterState {
  sensitiveVisible: boolean
  keys: LogValueFilterKey[]
  values: Partial<Record<LogValueFilterKey, LogFilterValue>>
  onFilter: (field: LogValueFilterKey, value: string) => void
}

const LogValueFilterContext = createContext<LogValueFilterState | null>(null)

export function LogValueFilterProvider(props: {
  enabled: boolean
  isAdmin: boolean
  values: LogValueFilterState['values']
  onFilter: LogValueFilterState['onFilter']
  children: ReactNode
}) {
  const { sensitiveVisible } = useUsageLogsContext()
  const value = useMemo(
    () =>
      props.enabled
        ? {
            sensitiveVisible,
            keys: getLogValueFilterKeys(props.isAdmin),
            values: props.values,
            onFilter: props.onFilter,
          }
        : null,
    [
      props.enabled,
      props.isAdmin,
      props.values,
      props.onFilter,
      sensitiveVisible,
    ]
  )
  return (
    <LogValueFilterContext.Provider value={value}>
      {props.children}
    </LogValueFilterContext.Provider>
  )
}

export function useLogValueFilter(
  field: LogValueFilterKey,
  value?: string,
  label?: string | null
) {
  const context = useContext(LogValueFilterContext)
  const { t } = useTranslation()
  if (
    !context ||
    !context.sensitiveVisible ||
    !value?.trim() ||
    !context.keys.includes(field)
  ) {
    return null
  }
  const active = getLogFilterValues(context.values[field]).includes(value)
  const title = `${t(active ? 'Remove filter' : 'Filter')} · ${t(logColumnFilterLabels[field])}: ${label || value}`
  const onClick: MouseEventHandler<HTMLElement> = (event) => {
    event.stopPropagation()
    context.onFilter(field, value)
  }
  return { active, onClick, 'aria-label': title, 'aria-pressed': active, title }
}

export function LogFilterValue(props: {
  field: LogValueFilterKey
  value?: string
  label?: string | null
  children: ReactNode
  className?: string
}) {
  const filter = useLogValueFilter(props.field, props.value, props.label)
  if (!filter) return props.children
  const { active, ...buttonProps } = filter
  return (
    <Button
      {...buttonProps}
      type='button'
      variant='ghost'
      className={cn(
        'h-auto max-w-full min-w-0 justify-start gap-1.5 rounded-sm p-0 text-left text-inherit text-[length:inherit] font-[weight:inherit] leading-[inherit] whitespace-normal hover:underline underline-offset-4',
        active && 'bg-primary/10 ring-primary/25 ring-1',
        props.className
      )}
    >
      {props.children}
    </Button>
  )
}

export function LogValueFilterChips() {
  const context = useContext(LogValueFilterContext)
  const { t } = useTranslation()
  const active =
    context?.keys.flatMap((key) =>
      getLogFilterValues(context.values[key]).map((value) => ({ key, value }))
    ) ?? []
  if (!context || !active.length) return null
  return (
    <div
      role='group'
      aria-label={t('Filters')}
      className='flex min-w-0 flex-wrap items-center gap-1.5 px-1 pb-2'
    >
      {active.map(({ key, value }) => {
        const label = `${t(logColumnFilterLabels[key])}: ${context.sensitiveVisible ? value : '••••'}`
        return (
          <Button
            key={JSON.stringify([key, value])}
            type='button'
            variant='secondary'
            size='xs'
            className='h-7 max-w-full min-w-0 gap-1.5 rounded-md text-xs font-normal'
            title={label}
            aria-label={`${t('Remove filter')} · ${label}`}
            onClick={() => context.onFilter(key, value)}
          >
            <span className='max-w-64 truncate'>{label}</span>
            <X aria-hidden className='size-3' />
          </Button>
        )
      })}
    </div>
  )
}
