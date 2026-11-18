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
import { zodResolver } from '@hookform/resolvers/zod'
import { Plus, Trash2 } from 'lucide-react'
import { Reorder } from 'motion/react'
import { useEffect, useMemo } from 'react'
import {
  useFieldArray,
  useForm,
  useFormContext,
  useWatch,
} from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { AutoGroupOrderItem } from '@/components/auto-group-order-item'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  parseRequestSourceRules,
  requestSourceRulesSchema,
  type RequestSourceRule,
  type RequestSourceRulesValues,
} from './request-source-rules'

const MATCH_LABELS = {
  exact: 'Exact header value',
  prefix: 'Header value prefix',
  contains: 'Contains',
  regex: 'Regular expression (RE2)',
} as const

function SourceRuleFields(props: { index: number }) {
  const { t } = useTranslation()
  const form = useFormContext<RequestSourceRulesValues>()
  const conditions = useFieldArray({
    control: form.control,
    name: `rules.${props.index}.conditions`,
  })
  return (
    <div className='grid w-full min-w-0 gap-4'>
      <div className='flex flex-wrap items-start gap-4'>
        <FormField
          control={form.control}
          name={`rules.${props.index}.name`}
          render={({ field }) => (
            <FormItem className='min-w-48 flex-1'>
              <FormLabel>{t('Source display name')}</FormLabel>
              <FormControl>
                <Input {...field} placeholder='Codex CLI' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={form.control}
          name={`rules.${props.index}.enabled`}
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Enabled')}</FormLabel>
              <FormControl>
                <Switch
                  checked={field.value}
                  onCheckedChange={field.onChange}
                />
              </FormControl>
            </FormItem>
          )}
        />
      </div>
      {conditions.fields.map((condition, index) => (
        <div
          key={condition.id}
          role='group'
          aria-label={t('Source match condition')}
          className='bg-muted/20 grid min-w-0 gap-3 rounded-lg border p-3 lg:grid-cols-2'
        >
          <FormField
            control={form.control}
            name={`rules.${props.index}.conditions.${index}.header`}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('HTTP header name')}</FormLabel>
                <FormControl>
                  <Input {...field} placeholder='User-Agent' />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name={`rules.${props.index}.conditions.${index}.match`}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Header match mode')}</FormLabel>
                <Select
                  value={field.value}
                  onValueChange={(value) =>
                    value !== null && field.onChange(value)
                  }
                  items={Object.entries(MATCH_LABELS).map(([value, label]) => ({
                    value,
                    label: t(label),
                  }))}
                >
                  <FormControl>
                    <SelectTrigger className='w-full'>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent>
                    {Object.entries(MATCH_LABELS).map(([value, label]) => (
                      <SelectItem key={value} value={value}>
                        {t(label)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name={`rules.${props.index}.conditions.${index}.value`}
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Header match value')}</FormLabel>
                <FormControl>
                  <Input {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <div className='flex items-center justify-between gap-3'>
            <FormField
              control={form.control}
              name={`rules.${props.index}.conditions.${index}.case_sensitive`}
              render={({ field }) => (
                <FormItem className='flex items-center gap-2'>
                  <FormControl>
                    <Switch
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                  <FormLabel>{t('Case-sensitive match')}</FormLabel>
                </FormItem>
              )}
            />
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              disabled={conditions.fields.length <= 1}
              aria-label={t('Remove condition')}
              onClick={() => conditions.remove(index)}
            >
              <Trash2 aria-hidden />
            </Button>
          </div>
        </div>
      ))}
      <Button
        type='button'
        variant='outline'
        size='sm'
        className='w-fit'
        disabled={conditions.fields.length >= 8}
        onClick={() =>
          conditions.append({
            header: 'User-Agent',
            match: 'prefix',
            value: '',
            case_sensitive: false,
          })
        }
      >
        <Plus aria-hidden />
        {t('Add condition')}
      </Button>
    </div>
  )
}

function SourceRulesEditor(props: { rules: RequestSourceRule[] }) {
  const { t, i18n } = useTranslation()
  const updateOption = useUpdateOption()
  const defaults = useMemo(() => ({ rules: props.rules }), [props.rules])
  const schema = useMemo(() => requestSourceRulesSchema(t), [t])
  const form = useForm<RequestSourceRulesValues>({
    resolver: zodResolver(schema),
    defaultValues: defaults,
  })
  useEffect(() => {
    form.reset(defaults)
  }, [defaults, form])
  const isDirty = form.formState.isDirty
  const rules = useFieldArray({ control: form.control, name: 'rules' })
  const values = useWatch({ control: form.control, name: 'rules' })
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const save = () => {
    void form
      .handleSubmit(async (values) => {
        await updateOption.mutateAsync({
          key: 'RequestSourceRules',
          value: JSON.stringify(values.rules),
        })
        form.reset(values)
      })()
      .catch(() => {
        /* The option mutation displays the server error; keep the draft. */
      })
  }
  return (
    <Form {...form}>
      <SettingsForm
        onSubmit={(event) => {
          event.preventDefault()
          save()
        }}
      >
        <SettingsPageFormActions
          onSave={save}
          onReset={() => form.reset()}
          isSaving={updateOption.isPending}
          isSaveDisabled={!isDirty}
          isResetDisabled={!isDirty}
        />
        <fieldset
          disabled={updateOption.isPending}
          className='min-w-0 space-y-4'
        >
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={rules.fields.length >= 100}
            onClick={() =>
              rules.append({
                enabled: true,
                name: '',
                conditions: [
                  {
                    header: 'User-Agent',
                    match: 'prefix',
                    value: '',
                    case_sensitive: false,
                  },
                ],
              })
            }
          >
            <Plus aria-hidden />
            {t('Add rule')}
          </Button>
          {rules.fields.length === 0 && (
            <p className='text-muted-foreground text-sm'>
              {t(
                'No custom rules. Built-in source recognition remains active.'
              )}
            </p>
          )}
          {form.formState.errors.rules?.message && (
            <p role='alert' className='text-destructive text-sm'>
              {form.formState.errors.rules.message}
            </p>
          )}
          <Reorder.Group
            as='ol'
            axis='y'
            aria-label={t('Source rule priority')}
            values={rules.fields.map((rule) => rule.id)}
            onReorder={(ids) => {
              if (updateOption.isPending) return
              const order = rules.fields.map((rule) => rule.id)
              for (let target = 0; target < ids.length; target++) {
                const from = order.indexOf(ids[target])
                if (from < 0 || from === target) continue
                rules.move(from, target)
                order.splice(target, 0, ...order.splice(from, 1))
              }
            }}
            className='flex flex-col gap-3'
          >
            {rules.fields.map((rule, index) => (
              <AutoGroupOrderItem
                key={rule.id}
                group={rule.id}
                label={
                  values[index]?.name ||
                  t('Source rule {{number}}', {
                    number: formatNumber(index + 1, locale),
                  })
                }
                index={index}
                count={rules.fields.length}
                onMove={(from, direction) => {
                  const target = from + (direction === 'up' ? -1 : 1)
                  if (target >= 0 && target < rules.fields.length) {
                    rules.move(from, target)
                  }
                }}
                onRemove={() => rules.remove(index)}
              >
                <SourceRuleFields index={index} />
              </AutoGroupOrderItem>
            ))}
          </Reorder.Group>
        </fieldset>
      </SettingsForm>
    </Form>
  )
}

export function RequestSourceRulesSection(props: { value: string }) {
  const { t } = useTranslation()
  const rules = useMemo(
    () => parseRequestSourceRules(props.value, t),
    [props.value, t]
  )
  return (
    <SettingsSection title={t('Request source recognition')}>
      <p className='text-muted-foreground text-sm'>
        {t(
          'The first matching enabled rule wins. All conditions in a rule must match. If none match, built-in recognition applies. Changes affect new requests only; other nodes refresh within 60 seconds.'
        )}
      </p>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Names are fixed display labels, not values extracted from headers. Sensitive headers cannot be used. Regular expressions are validated by the server using RE2.'
        )}
      </p>
      {rules === null ? (
        <>
          <SettingsPageFormActions onSave={() => {}} isSaveDisabled />
          <Alert variant='destructive'>
            <AlertDescription>
              {t(
                'Saved source rules could not be read. No data was changed. Correct the stored configuration before editing.'
              )}
            </AlertDescription>
          </Alert>
        </>
      ) : (
        <SourceRulesEditor rules={rules} />
      )}
    </SettingsSection>
  )
}
