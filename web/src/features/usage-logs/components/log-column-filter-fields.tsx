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
import { useQuery } from '@tanstack/react-query'
import { useId, type KeyboardEventHandler } from 'react'
import { useTranslation } from 'react-i18next'

import { ComboboxInput } from '@/components/ui/combobox-input'
import { getGroups } from '@/features/users/api'
import { useModelAccessScope } from '@/hooks/use-model-access-scope'
import { getUserGroups } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { useLogUserOptions } from '../hooks/use-log-user-options'
import {
  getLogColumnFilterKeys,
  isValidLogChannelFilter,
  type LogColumnFilterKey,
  type LogColumnFilterValues,
  type LogFilterColumn,
} from '../lib/filter'
import { LogChoiceFilter } from './log-choice-filter'
import { LogsFilterField, LogsFilterInput } from './logs-filter-toolbar'

type FilterInputProps = {
  caption?: { id: string; text: string }
  value?: string
  onValueChange: (value: string) => void
  onKeyDown?: KeyboardEventHandler<HTMLInputElement>
  maskClass?: string
  inlineSuggestions?: boolean
}

export function LogUsernameFilter(props: FilterInputProps) {
  const { t } = useTranslation()
  const { users, loading, setSearch } = useLogUserOptions()
  return (
    <LogsFilterField caption={props.caption} className={props.maskClass}>
      <ComboboxInput
        id={props.caption?.id}
        inlineSuggestions={props.inlineSuggestions}
        openOnFocus={!props.inlineSuggestions}
        options={users.map(({ username, label }) => ({
          label,
          value: username,
        }))}
        allowCustomValue
        showEmptyOnOpen
        aria-label={t('Username')}
        placeholder={props.caption ? t('All users') : t('Username')}
        emptyText={loading ? t('Loading...') : t('No matching results')}
        className='h-8 min-w-0 text-sm leading-5'
        popupClassName={props.maskClass}
        value={props.value}
        onSearchChange={setSearch}
        onValueChange={props.onValueChange}
        onKeyDown={props.onKeyDown}
      />
    </LogsFilterField>
  )
}

export function LogGroupFilter(props: FilterInputProps & { isAdmin: boolean }) {
  const { t } = useTranslation()
  const scope = useModelAccessScope()
  const adminGroups = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    enabled: props.isAdmin,
  })
  const userGroups = useQuery({
    queryKey: ['user-groups', ...scope],
    queryFn: async ({ signal }) =>
      requireServerSuccess(await getUserGroups(signal)),
    enabled: !props.isAdmin,
  })
  const groups = props.isAdmin
    ? (adminGroups.data?.data ?? [])
    : Object.keys(userGroups.data?.data ?? {})
  const loading = props.isAdmin ? adminGroups.isLoading : userGroups.isLoading

  return (
    <LogsFilterField caption={props.caption} className={props.maskClass}>
      <ComboboxInput
        id={props.caption?.id}
        inlineSuggestions={props.inlineSuggestions}
        openOnFocus={!props.inlineSuggestions}
        options={groups
          .filter((group) => group !== 'auto')
          .map((group) => ({
            label: group,
            value: group,
          }))}
        allowCustomValue
        showEmptyOnOpen
        aria-label={t('Group')}
        placeholder={props.caption ? t('All Groups') : t('Group')}
        emptyText={loading ? t('Loading...') : t('No group found.')}
        className='h-8 min-w-0 text-sm leading-5'
        popupClassName={props.maskClass}
        value={props.value}
        onValueChange={props.onValueChange}
        onKeyDown={props.onKeyDown}
      />
    </LogsFilterField>
  )
}

const effortSuggestions = [
  'disabled',
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
]
const fieldLabels = {
  type: 'Type',
  responseTransport: 'Response mode',
  channel: 'Channel ID',
  token: 'Token Name',
  username: 'Username',
  group: 'Group',
  model: 'Model Name',
  reasoningEffort: 'Reasoning Effort',
  clientTool: 'Client',
  ip: 'IP Address',
  requestPath: 'Endpoint',
} as const

export function LogColumnFilterFields(props: {
  column: LogFilterColumn
  isAdmin: boolean
  values: LogColumnFilterValues
  onChange: (key: LogColumnFilterKey, value: string) => void
  onKeyDown?: KeyboardEventHandler<HTMLInputElement>
  maskClass?: string
  showLabels?: boolean
  inlineSuggestions?: boolean
  suggestions?: Partial<Record<LogColumnFilterKey, string[]>>
}) {
  const { t } = useTranslation()
  const id = useId()
  return (
    <>
      {getLogColumnFilterKeys(props.column, props.isAdmin).map((key) => {
        const label = t(fieldLabels[key])
        const caption = props.showLabels
          ? { id: `${id}-${key}`, text: label }
          : undefined
        const input = {
          caption,
          value: props.values[key] ?? '',
          onValueChange: (value: string) => props.onChange(key, value),
          onKeyDown: props.onKeyDown,
          maskClass: props.maskClass,
          inlineSuggestions: props.inlineSuggestions,
        }
        if (key === 'type' || key === 'responseTransport') {
          return (
            <LogChoiceFilter
              key={key}
              caption={caption}
              field={key}
              value={input.value}
              onChange={input.onValueChange}
            />
          )
        }
        if (key === 'username') {
          return <LogUsernameFilter key={key} {...input} />
        }
        if (key === 'group') {
          return <LogGroupFilter key={key} {...input} isAdmin={props.isAdmin} />
        }

        const invalid =
          key === 'channel' && !isValidLogChannelFilter(input.value)
        const placeholder = props.showLabels ? t('Search...') : label
        const mask = ['token', 'clientTool', 'ip'].includes(key)
          ? props.maskClass
          : undefined
        const suggestions = props.suggestions?.[key] ?? []
        const options =
          key === 'reasoningEffort'
            ? [...new Set([...effortSuggestions, ...suggestions])]
            : suggestions

        return (
          <LogsFilterField key={key} caption={caption}>
            {options.length ? (
              <ComboboxInput
                id={caption?.id}
                inlineSuggestions={props.inlineSuggestions}
                openOnFocus={!props.inlineSuggestions}
                allowCustomValue
                aria-label={label}
                aria-invalid={invalid || undefined}
                placeholder={placeholder}
                options={options.map((value) => ({ value, label: value }))}
                className={mask ? `h-8 text-sm ${mask}` : 'h-8 text-sm'}
                popupClassName={mask}
                value={input.value}
                onValueChange={input.onValueChange}
                onKeyDown={input.onKeyDown}
              />
            ) : (
              <LogsFilterInput
                id={caption?.id}
                aria-label={label}
                aria-invalid={invalid || undefined}
                placeholder={placeholder}
                inputMode={key === 'channel' ? 'numeric' : undefined}
                className={mask}
                value={input.value}
                onChange={(event) => input.onValueChange(event.target.value)}
                onKeyDown={input.onKeyDown}
              />
            )}
            {invalid && (
              <p role='alert' className='text-destructive mt-1 text-xs'>
                {t('Enter a positive integer')}
              </p>
            )}
          </LogsFilterField>
        )
      })}
    </>
  )
}
