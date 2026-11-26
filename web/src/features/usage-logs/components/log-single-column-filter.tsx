import { ChevronDown } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Combobox,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
} from '@/components/ui/combobox'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
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
import { cn } from '@/lib/utils'

import { LOG_RESPONSE_TRANSPORTS } from '../constants'
import { useLogGroupOptions } from '../hooks/use-log-group-options'
import { useLogUserOptions } from '../hooks/use-log-user-options'
import {
  effortSuggestions,
  getLogFilterValues,
  normalizeLogFilterValue,
  isValidLogChannelFilter,
  logColumnFilterLabels,
  type LogColumnFilterKey,
} from '../lib/filter'
import type { LogFilterValue } from '../types'
import { LogsFilterField } from './logs-filter-toolbar'

export function LogSingleColumnFilter(props: {
  field: LogColumnFilterKey
  value?: LogFilterValue
  suggestions?: string[]
  isAdmin: boolean
  maskClass?: string
  onSelect: (
    value: LogFilterValue | undefined,
    labels?: Record<string, string>
  ) => void
}) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const query = search.trim()
  const selected = getLogFilterValues(props.value)
  const {
    users,
    loading: usersLoading,
    setSearch: searchUsers,
  } = useLogUserOptions()
  const { groups, loading: groupsLoading } = useLogGroupOptions(
    props.isAdmin,
    props.field === 'group'
  )
  useEffect(() => {
    searchUsers(props.field === 'username' ? query : null)
  }, [props.field, query, searchUsers])

  let options = (props.suggestions ?? []).map((value) => ({
    value,
    label: value,
  }))
  let loading = false
  if (props.field === 'username') {
    options = users.map(({ username, label }) => ({ value: username, label }))
    loading = usersLoading
  } else if (props.field === 'group') {
    options = groups
      .filter((group) => group !== 'auto')
      .map((group) => ({ value: group, label: group }))
    loading = groupsLoading
  } else if (props.field === 'reasoningEffort') {
    options = [
      ...new Set([...effortSuggestions, ...(props.suggestions ?? [])]),
    ].map((value) => ({ value, label: value }))
  }
  if (props.field === 'responseTransport') {
    const labels = {
      sse: 'SSE',
      ws: 'WS',
      http: 'HTTP',
      sync: t('Sync'),
      async: t('Async'),
      unknown: t('Protocol not recorded'),
      non_streamed: t('Non-streamed response'),
    }
    options = LOG_RESPONSE_TRANSPORTS.map((value) => ({
      value,
      label: labels[value],
    }))
  }
  for (const value of selected) {
    if (props.field === 'group' && value === 'auto') continue
    if (!options.some((option) => option.value === value)) {
      options.unshift({ value, label: value })
    }
  }
  const matches = options.filter((option) =>
    `${option.label} ${option.value}`
      .toLowerCase()
      .includes(query.toLowerCase())
  )
  const invalid = props.field === 'channel' && !isValidLogChannelFilter(query)
  const custom =
    props.field !== 'responseTransport' &&
    query &&
    !invalid &&
    !matches.some((option) => option.value === query)
  const label = t(logColumnFilterLabels[props.field])
  const items = [
    ...(!query ? [''] : []),
    ...matches.map((option) => option.value),
    ...(custom ? [query] : []),
  ]
  const labels = Object.fromEntries(
    options.map((option) => [option.value, option.label])
  )
  return (
    <div className={cn('flex min-w-0 flex-col p-1', props.maskClass)}>
      <Combobox<string, true>
        multiple
        inline
        open
        autoHighlight
        items={items}
        filter={null}
        value={selected.length ? selected : ['']}
        inputValue={search}
        onInputValueChange={(value, details) => {
          if (details.reason === 'input-change') setSearch(value)
        }}
        onValueChange={(values) => {
          const next =
            values.includes('') && selected.length
              ? []
              : values.filter((value) => value !== '')
          props.onSelect(normalizeLogFilterValue(next), labels)
          setSearch('')
        }}
      >
        <ComboboxInput
          aria-label={label}
          aria-invalid={invalid || undefined}
          placeholder={label}
          showTrigger={false}
          className='border-input/30 bg-input/30 m-1 h-8 rounded-lg shadow-none'
        />
        <ComboboxList className='max-h-72'>
          {items.map((value) => (
            <ComboboxItem
              key={value}
              value={value}
              data-checked={
                value === '' ? selected.length === 0 : selected.includes(value)
              }
            >
              <span className='min-w-0 truncate' title={labels[value]}>
                {value === ''
                  ? t('All')
                  : (labels[value] ?? `${t('Search')} · ${value}`)}
              </span>
            </ComboboxItem>
          ))}
          {items.length === 0 && (
            <p className='text-muted-foreground px-2 py-4 text-center text-sm'>
              {loading ? t('Loading...') : t('No matching results')}
            </p>
          )}
        </ComboboxList>
      </Combobox>
      <div className='flex items-center justify-between gap-2 border-t px-2 py-1'>
        <span className='text-muted-foreground text-xs'>
          {t('{{count}} selected', { count: selected.length })}
        </span>
        <Button
          type='button'
          variant='ghost'
          size='xs'
          disabled={selected.length === 0}
          onClick={() => {
            props.onSelect(undefined)
            setSearch('')
          }}
        >
          {t('Clear')}
        </Button>
      </div>
      {invalid && (
        <p role='alert' className='text-destructive px-2 pb-2 text-xs'>
          {t('Enter a positive integer')}
        </p>
      )}
    </div>
  )
}

export function LogMultipleColumnField(props: {
  field: LogColumnFilterKey
  value?: LogFilterValue
  suggestions?: string[]
  isAdmin: boolean
  maskClass?: string
  caption?: { id: string; text: string }
  onSelect: (value: LogFilterValue | undefined) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [selectedLabels, setSelectedLabels] = useState<Record<string, string>>(
    {}
  )
  const selected = getLogFilterValues(props.value)
  const label = t(logColumnFilterLabels[props.field])
  let display = label
  if (selected.length === 1) {
    const value = selected[0]
    const transportLabels: Record<string, string> = {
      sse: 'SSE',
      ws: 'WS',
      http: 'HTTP',
      sync: t('Sync'),
      async: t('Async'),
      unknown: t('Protocol not recorded'),
      non_streamed: t('Non-streamed response'),
    }
    display =
      props.field === 'responseTransport'
        ? (transportLabels[value] ?? value)
        : (selectedLabels[value] ?? value)
    if (props.field === 'username' && display.endsWith(` · ${value}`)) {
      display = display.slice(0, -` · ${value}`.length)
    }
  } else if (selected.length > 1) {
    display = t('{{count}} selected', { count: selected.length })
  }
  return (
    <LogsFilterField caption={props.caption}>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={
            <Button
              id={props.caption?.id}
              type='button'
              role='combobox'
              aria-label={label}
              aria-expanded={open}
              variant='outline'
              className={cn(
                'h-8 w-full min-w-0 justify-between rounded-md px-3 text-sm leading-5 font-normal',
                props.maskClass
              )}
            />
          }
        >
          <span className='truncate'>{display}</span>
          <ChevronDown
            aria-hidden
            className='text-muted-foreground size-3.5 shrink-0'
          />
        </PopoverTrigger>
        <PopoverContent
          align='start'
          className='w-60 max-w-(--available-width) gap-0 p-0'
        >
          <PopoverTitle className='sr-only'>{label}</PopoverTitle>
          <LogSingleColumnFilter
            {...props}
            onSelect={(value, labels) => {
              if (labels) setSelectedLabels(labels)
              props.onSelect(value)
            }}
          />
        </PopoverContent>
      </Popover>
    </LogsFilterField>
  )
}
