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
import { useId, useMemo } from 'react'
import { useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import {
  FormDescription,
  FormField,
  FormItem,
  FormMessage,
} from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import {
  normalizeChannelGroupModels,
  parseModels,
  type ChannelFormValues,
} from '../lib/channel-form'

export function ChannelGroupModelsSettings(props: {
  control: Control<ChannelFormValues>
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const id = useId()
  const groups = useWatch({ control: props.control, name: 'group' })
  const modelList = useWatch({ control: props.control, name: 'models' })
  const models = useMemo(() => parseModels(modelList), [modelList])
  const options = useMemo(
    () => models.map((model) => ({ value: model, label: model })),
    [models]
  )

  return (
    <section
      className='space-y-3 border-t pt-4'
      aria-label={t('Models by group')}
    >
      <FormField
        control={props.control}
        name='group_models'
        render={({ field }) => {
          const scoped = normalizeChannelGroupModels(
            field.value,
            groups,
            models
          )
          return (
            <FormItem className='space-y-3'>
              <p className='text-sm font-medium'>{t('Models by group')}</p>
              <FormDescription>
                {t(
                  'Each group includes all channel models unless limited below. These limits apply to model listings and requests.'
                )}
              </FormDescription>
              {groups.map((group, index) => {
                const allModels = !Object.hasOwn(scoped, group)
                const switchId = `${id}-${index}-all`
                return (
                  <fieldset
                    key={group}
                    className='min-w-0 space-y-3 rounded-md border p-3'
                    disabled={props.disabled}
                  >
                    <legend className='max-w-full px-1 text-sm font-medium break-all'>
                      {group}
                    </legend>
                    <div className='flex items-center justify-between gap-3'>
                      <Label htmlFor={switchId}>
                        {t('All channel models')}
                      </Label>
                      <Switch
                        id={switchId}
                        checked={allModels}
                        disabled={props.disabled}
                        onCheckedChange={(checked) => {
                          const next = { ...scoped, [group]: [] }
                          if (checked) delete next[group]
                          field.onChange(next)
                        }}
                      />
                    </div>
                    {!allModels && (
                      <>
                        <MultiSelect
                          options={options}
                          selected={scoped[group]}
                          onChange={(selected) =>
                            field.onChange({ ...scoped, [group]: selected })
                          }
                          placeholder={t('Select models for {{group}}', {
                            group,
                          })}
                          maxVisibleChips={6}
                          disabled={props.disabled}
                        />
                        {scoped[group].length === 0 && (
                          <p className='text-muted-foreground text-xs'>
                            {t(
                              'This group has no available models on this channel.'
                            )}
                          </p>
                        )}
                      </>
                    )}
                  </fieldset>
                )
              })}
              <FormMessage />
            </FormItem>
          )
        }}
      />
    </section>
  )
}
