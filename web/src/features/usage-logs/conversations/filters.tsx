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
import { ListFilter } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'

import { useLogUserOptions } from '../hooks/use-log-user-options'
import {
  getConversationClients,
  type ConversationFilters,
  type ConversationSort,
} from './api'

const unknownClient = '\u0000unknown'
export function ConversationFilterBar(props: {
  filters: ConversationFilters
  sort: ConversationSort
  viewer: number
  onFilter: (filters: ConversationFilters) => void
  onSort: (sort: ConversationSort) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<ConversationFilters>({})
  const [userLabel, setUserLabel] = useState<{ id: number; label: string }>()
  const { users, loading, setSearch, keyword } = useLogUserOptions()
  const clients = useQuery({
    queryKey: ['saved-conversations', props.viewer, 'clients'],
    queryFn: ({ signal }) => getConversationClients(signal),
    enabled: open,
    staleTime: 60_000,
    meta: { errorToast: false },
  })

  const userOptions = users.map(({ id, username }) => ({
    label: username,
    value: String(id),
  }))
  if (userLabel && !userOptions.some((o) => o.value === String(userLabel.id))) {
    userOptions.push({ value: String(userLabel.id), label: userLabel.label })
  }
  // Older captures can outlive the account referenced by their numeric ID.
  if (
    /^[1-9]\d*$/.test(keyword) &&
    Number.isSafeInteger(Number(keyword)) &&
    !userOptions.some((o) => o.value === keyword)
  ) {
    userOptions.push({ value: keyword, label: `${t('User ID')} ${keyword}` })
  }
  const sorts = [
    {
      sort_by: 'updated_at',
      sort_order: 'desc',
      label: t('Most recently updated'),
    },
    {
      sort_by: 'updated_at',
      sort_order: 'asc',
      label: t('Least recently updated'),
    },
    {
      sort_by: 'started_at',
      sort_order: 'desc',
      label: t('Newest conversations'),
    },
    {
      sort_by: 'started_at',
      sort_order: 'asc',
      label: t('Oldest conversations'),
    },
    { sort_by: 'turn_count', sort_order: 'desc', label: t('Most turns first') },
    {
      sort_by: 'turn_count',
      sort_order: 'asc',
      label: t('Fewest turns first'),
    },
  ] satisfies (ConversationSort & { label: string })[]
  const inverted =
    draft.min_turns !== undefined &&
    draft.max_turns !== undefined &&
    draft.min_turns > draft.max_turns
  const filterCount = [
    props.filters.user_id,
    props.filters.model_name,
    props.filters.client_tool || props.filters.client_tool_unknown,
    props.filters.min_turns,
    props.filters.max_turns,
  ].filter((v) => v !== undefined && v !== '').length

  return (
    <div className='bg-card shrink-0 space-y-2 border-b px-3 py-2.5'>
      <div className='flex min-w-0 flex-wrap items-center gap-2'>
        <NativeSelect
          aria-label={t('Sort')}
          className='min-w-0 flex-1'
          value={`${props.sort.sort_by}:${props.sort.sort_order}`}
          onChange={(e) => {
            const selected = sorts.find(
              (s) => `${s.sort_by}:${s.sort_order}` === e.target.value
            )
            if (selected) {
              props.onSort({
                sort_by: selected.sort_by,
                sort_order: selected.sort_order,
              })
            }
          }}
        >
          {sorts.map((s) => (
            <NativeSelectOption
              key={s.sort_by + s.sort_order}
              value={`${s.sort_by}:${s.sort_order}`}
            >
              {s.label}
            </NativeSelectOption>
          ))}
        </NativeSelect>
        <Dialog
          open={open}
          onOpenChange={(next) => {
            if (next) setDraft(props.filters)
            setOpen(next)
          }}
          title={t('Filters')}
          contentClassName='sm:max-w-md'
          trigger={
            <Button
              type='button'
              variant={filterCount ? 'secondary' : 'outline'}
              size='sm'
            >
              <ListFilter aria-hidden='true' /> {t('Filter')}
              {filterCount > 0 && <span>({filterCount})</span>}
            </Button>
          }
          footer={
            <>
              <Button
                type='button'
                variant='outline'
                onClick={() => {
                  props.onFilter({})
                  setOpen(false)
                }}
              >
                {t('Clear filters')}
              </Button>
              <Button form={`${id}-form`} type='submit' disabled={inverted}>
                {t('Apply Filters')}
              </Button>
            </>
          }
        >
          {open && (
            <form
              id={`${id}-form`}
              className='grid grid-cols-2 gap-4'
              onSubmit={(event) => {
                event.preventDefault()
                if (inverted) return
                props.onFilter({
                  ...draft,
                  model_name: draft.model_name?.trim() || undefined,
                  client_tool: draft.client_tool?.trim() || undefined,
                })
                setOpen(false)
              }}
            >
              <Field className='col-span-2'>
                <FieldLabel htmlFor={`${id}-user`}>{t('User')}</FieldLabel>
                <ComboboxInput
                  id={`${id}-user`}
                  openOnFocus={false}
                  placeholder={t('All users')}
                  value={draft.user_id ? String(draft.user_id) : ''}
                  options={[
                    { value: '', label: t('All users') },
                    ...userOptions,
                  ]}
                  showEmptyOnOpen
                  emptyText={
                    loading ? t('Loading...') : t('No matching results')
                  }
                  onSearchChange={setSearch}
                  onValueChange={(value) => {
                    setDraft((v) => ({
                      ...v,
                      user_id: value ? Number(value) : undefined,
                    }))
                    const choice = userOptions.find((o) => o.value === value)
                    if (choice) {
                      setUserLabel({ id: Number(value), label: choice.label })
                    }
                  }}
                />
              </Field>
              <Field className='col-span-2'>
                <FieldLabel htmlFor={`${id}-client`}>
                  {t('Request client')}
                </FieldLabel>
                <ComboboxInput
                  id={`${id}-client`}
                  maxLength={64}
                  allowCustomValue
                  showEmptyOnOpen
                  placeholder={t('All')}
                  emptyText={
                    clients.isFetching
                      ? t('Loading...')
                      : t('No matching results')
                  }
                  options={[
                    { value: '', label: t('All') },
                    { value: unknownClient, label: t('Unidentified source') },
                    ...(clients.data ?? []).map((client) => ({
                      value: client,
                      label: client,
                    })),
                  ]}
                  value={
                    draft.client_tool_unknown
                      ? t('Unidentified source')
                      : draft.client_tool
                  }
                  onValueChange={(value) => {
                    const unknown =
                      value === unknownClient ||
                      (draft.client_tool_unknown &&
                        value === t('Unidentified source'))
                    setDraft({
                      ...draft,
                      client_tool: unknown ? undefined : value || undefined,
                      client_tool_unknown: unknown || undefined,
                    })
                  }}
                />
              </Field>
              <Field className='col-span-2'>
                <FieldLabel htmlFor={`${id}-model`}>
                  {t('Model Name')}
                </FieldLabel>
                <Input
                  id={`${id}-model`}
                  placeholder={t('All models')}
                  maxLength={192}
                  value={draft.model_name ?? ''}
                  onChange={(e) =>
                    setDraft({ ...draft, model_name: e.target.value })
                  }
                />
              </Field>
              {(['min_turns', 'max_turns'] as const).map((key) => (
                <Field key={key} data-invalid={inverted || undefined}>
                  <FieldLabel htmlFor={id + key}>
                    {key === 'min_turns'
                      ? t('Minimum turns')
                      : t('Maximum turns')}
                  </FieldLabel>
                  <Input
                    id={id + key}
                    min={1}
                    max={Number.MAX_SAFE_INTEGER}
                    step={1}
                    type='number'
                    inputMode='numeric'
                    value={draft[key] ?? ''}
                    aria-invalid={inverted || undefined}
                    aria-describedby={
                      inverted ? `${id}-range-error` : undefined
                    }
                    onChange={(e) => {
                      const value = e.currentTarget.valueAsNumber
                      setDraft({
                        ...draft,
                        [key]: Number.isFinite(value) ? value : undefined,
                      })
                    }}
                  />
                </Field>
              ))}
              {inverted && (
                <FieldError id={`${id}-range-error`} className='col-span-2'>
                  {t('Minimum turns must not exceed maximum turns.')}
                </FieldError>
              )}
            </form>
          )}
        </Dialog>
      </div>
      <Label className='text-muted-foreground flex w-fit cursor-pointer gap-2 text-xs font-normal'>
        <Switch
          size='sm'
          checked={!!props.filters.hide_greetings}
          onCheckedChange={(checked) =>
            props.onFilter({
              ...props.filters,
              hide_greetings: checked || undefined,
            })
          }
        />
        {t('Hide greeting-only conversations')}
      </Label>
    </div>
  )
}
