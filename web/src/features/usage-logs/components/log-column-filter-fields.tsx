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
import { useId, type KeyboardEventHandler } from 'react'
import { useTranslation } from 'react-i18next'

import { ComboboxInput } from '@/components/ui/combobox-input'

import { useLogGroupOptions } from '../hooks/use-log-group-options'
import { useLogUserOptions } from '../hooks/use-log-user-options'
import {
  effortSuggestions,
  getLogFilterValues,
  getLogColumnFilterKeys,
  logColumnFilterLabels,
  isValidLogChannelFilter,
  type LogColumnFilterKey,
  type LogColumnFilterValues,
  type LogFilterColumn,
} from '../lib/filter'
import type { LogFilterValue } from '../types'
import { LogChoiceFilter } from './log-choice-filter'
import { LogMultipleColumnField } from './log-single-column-filter'
import { LogsFilterField, LogsFilterInput } from './logs-filter-toolbar'

type FilterInputProps = {
  caption?: { id: string; text: string }
  value?: LogFilterValue
  multiple?: boolean
  onValueChange: (value: LogFilterValue | undefined) => void
  onKeyDown?: KeyboardEventHandler<HTMLInputElement>
  maskClass?: string
  inlineSuggestions?: boolean
}

export function LogUsernameFilter(props: FilterInputProps) {
  const { t } = useTranslation()
  const { users, loading, setSearch } = useLogUserOptions()
  if (props.multiple) {
    return (
      <LogMultipleColumnField
        field='username'
        {...props}
        isAdmin
        onSelect={props.onValueChange}
      />
    )
  }
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
        value={getLogFilterValues(props.value)[0] ?? ''}
        onSearchChange={setSearch}
        onValueChange={props.onValueChange}
        onKeyDown={props.onKeyDown}
      />
    </LogsFilterField>
  )
}

export function LogGroupFilter(props: FilterInputProps & { isAdmin: boolean }) {
  const { t } = useTranslation()
  const { groups, loading } = useLogGroupOptions(props.isAdmin)
  if (props.multiple) {
    return (
      <LogMultipleColumnField
        field='group'
        {...props}
        onSelect={props.onValueChange}
      />
    )
  }

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
        value={getLogFilterValues(props.value)[0] ?? ''}
        onValueChange={props.onValueChange}
        onKeyDown={props.onKeyDown}
      />
    </LogsFilterField>
  )
}

export function LogColumnFilterFields(props: {
  column: LogFilterColumn
  isAdmin: boolean
  values: LogColumnFilterValues
  onChange: (key: LogColumnFilterKey, value: LogFilterValue | undefined) => void
  multiple?: boolean
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
        const label = t(logColumnFilterLabels[key])
        const caption = props.showLabels
          ? { id: `${id}-${key}`, text: label }
          : undefined
        if (props.multiple && key !== 'type') {
          return (
            <LogMultipleColumnField
              key={key}
              field={key}
              caption={caption}
              value={props.values[key]}
              suggestions={props.suggestions?.[key]}
              isAdmin={props.isAdmin}
              maskClass={
                ['username', 'token', 'group', 'clientTool', 'ip'].includes(key)
                  ? props.maskClass
                  : undefined
              }
              onSelect={(value) => props.onChange(key, value)}
            />
          )
        }
        const input = {
          caption,
          value: getLogFilterValues(props.values[key])[0] ?? '',
          onValueChange: (value: LogFilterValue | undefined) =>
            props.onChange(key, value),
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
