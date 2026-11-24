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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import {
  LOG_RESPONSE_TRANSPORTS,
  LOG_TYPE_ALL_VALUE,
  LOG_TYPE_FILTERS,
} from '../constants'
import { LogsFilterField } from './logs-filter-toolbar'
import { useLogsViewScope } from './usage-logs-provider'

export function LogChoiceFilter(props: {
  field: 'type' | 'responseTransport'
  value?: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const { canManageScope } = useLogsViewScope()
  const label = props.field === 'type' ? t('Type') : t('Response mode')
  const defaultValue = props.field === 'type' ? LOG_TYPE_ALL_VALUE : ''
  const value = props.value || defaultValue
  let items: { value: string; label: string; deprecated?: boolean }[]
  if (props.field === 'type') {
    items = LOG_TYPE_FILTERS.filter(
      (item) => !item.deprecated || canManageScope
    ).map((item) => ({ ...item, label: t(item.label) }))
  } else {
    const labels = {
      sse: 'SSE',
      ws: 'WS',
      http: 'HTTP',
      sync: t('Sync'),
      async: t('Async'),
      unknown: t('Protocol not recorded'),
      non_streamed: t('Non-streamed response'),
    }
    items = [
      { value: '', label: t('All') },
      ...LOG_RESPONSE_TRANSPORTS.map((value) => ({
        value,
        label: labels[value],
      })),
    ]
  }
  const selected = items.find((item) => item.value === value)
  const deprecatedTypeDescription = t(
    'Only used to find historical logs. New records are available in Audit Logs.'
  )
  return (
    <LogsFilterField>
      <Select
        items={items}
        value={value}
        onValueChange={(next) => props.onChange(next ?? defaultValue)}
      >
        <SelectTrigger
          aria-label={label}
          aria-description={
            selected?.deprecated ? deprecatedTypeDescription : undefined
          }
        >
          <SelectValue className='min-w-0'>
            <span className='truncate'>
              {(value === '' ? label : selected?.label) ?? label}
            </span>
            {selected?.deprecated && (
              <Badge
                variant='secondary'
                className='h-4 px-1.5 text-[10px] font-normal'
                title={deprecatedTypeDescription}
              >
                {t('Deprecated')}
              </Badge>
            )}
          </SelectValue>
        </SelectTrigger>
        <SelectContent
          alignItemWithTrigger={false}
          className='max-w-[calc(100vw-2rem)] min-w-52'
        >
          <SelectGroup>
            {items.map((type) => (
              <SelectItem
                key={type.value}
                value={type.value}
                className='[&_[data-slot=select-item-text]]:items-center'
                aria-description={
                  type.deprecated ? deprecatedTypeDescription : undefined
                }
              >
                {type.label}
                {type.deprecated && (
                  <Badge
                    variant='secondary'
                    className='h-4 px-1.5 text-[10px] font-normal'
                    title={deprecatedTypeDescription}
                  >
                    {t('Deprecated')}
                  </Badge>
                )}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
    </LogsFilterField>
  )
}
