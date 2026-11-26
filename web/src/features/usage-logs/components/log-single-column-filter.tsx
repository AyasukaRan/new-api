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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'

import { useLogGroupOptions } from '../hooks/use-log-group-options'
import { useLogUserOptions } from '../hooks/use-log-user-options'
import {
  effortSuggestions,
  isValidLogChannelFilter,
  logColumnFilterLabels,
  type LogColumnFilterKey,
} from '../lib/filter'

export function LogSingleColumnFilter(props: {
  field: LogColumnFilterKey
  value?: string
  suggestions?: string[]
  isAdmin: boolean
  maskClass?: string
  onSelect: (value: string) => void
}) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const query = search.trim()
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
  if (props.value && !options.some((option) => option.value === props.value)) {
    options.unshift({ value: props.value, label: props.value })
  }
  const matches = options.filter((option) =>
    `${option.label} ${option.value}`
      .toLowerCase()
      .includes(query.toLowerCase())
  )
  const invalid = props.field === 'channel' && !isValidLogChannelFilter(query)
  const custom =
    query && !invalid && !matches.some((option) => option.value === query)
  const label = t(logColumnFilterLabels[props.field])
  return (
    <Command label={label} shouldFilter={false} className={props.maskClass}>
      <CommandInput
        aria-label={label}
        aria-invalid={invalid || undefined}
        placeholder={label}
        value={search}
        onValueChange={setSearch}
      />
      <CommandList>
        <CommandEmpty>
          {loading ? t('Loading...') : t('No matching results')}
        </CommandEmpty>
        <CommandGroup>
          {!query && (
            <CommandItem
              value='__all'
              data-checked={!props.value}
              onSelect={() => props.onSelect('')}
            >
              {t('All')}
            </CommandItem>
          )}
          {matches.map((option) => (
            <CommandItem
              key={option.value}
              value={option.value}
              data-checked={option.value === props.value}
              onSelect={() => props.onSelect(option.value)}
            >
              <span className='min-w-0 truncate' title={option.label}>
                {option.label}
              </span>
            </CommandItem>
          ))}
          {custom && (
            <CommandItem value={query} onSelect={() => props.onSelect(query)}>
              <span className='min-w-0 truncate'>
                {t('Search')} · {query}
              </span>
            </CommandItem>
          )}
        </CommandGroup>
      </CommandList>
      {invalid && (
        <p role='alert' className='text-destructive px-2 pb-2 text-xs'>
          {t('Enter a positive integer')}
        </p>
      )}
    </Command>
  )
}
