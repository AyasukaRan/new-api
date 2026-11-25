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
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, Plus } from 'lucide-react'
import { Reorder } from 'motion/react'
import { nanoid } from 'nanoid'
import { useEffect, useMemo, useState } from 'react'
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
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { ErrorHistoryInventory } from './error-recognition-history'
import { RulePreview } from './error-recognition-preview'
import {
  errorRecognitionFormSchema,
  parseErrorRecognitionRules,
  serializeErrorRecognitionRules,
  type ErrorRecognitionFormValues,
  type ErrorRecognitionRule,
} from './error-recognition-rules'

function ErrorRuleFields(props: { index: number }) {
  const { t } = useTranslation()
  const form = useFormContext<ErrorRecognitionFormValues>()
  const matchModes = [
    { value: 'contains', label: t('Contains') },
    { value: 'exact', label: t('Exact error text') },
    { value: 'regex', label: t('Regular expression (RE2)') },
  ]
  return (
    <div className='grid min-w-0 gap-4 sm:grid-cols-2'>
      <FormField
        control={form.control}
        name={`rules.${props.index}.name`}
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Rule ID')}</FormLabel>
            <FormControl>
              <Input {...field} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name={`rules.${props.index}.label`}
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Short error label')}</FormLabel>
            <FormControl>
              <Input {...field} value={t(field.value)} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name={`rules.${props.index}.match`}
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Error match mode')}</FormLabel>
            <Select
              value={field.value}
              onValueChange={(value) => value !== null && field.onChange(value)}
              items={matchModes}
            >
              <FormControl>
                <SelectTrigger className='w-full'>
                  <SelectValue />
                </SelectTrigger>
              </FormControl>
              <SelectContent>
                {matchModes.map((mode) => (
                  <SelectItem key={mode.value} value={mode.value}>
                    {mode.label}
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
        name={`rules.${props.index}.status_codes`}
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Status codes (optional)')}</FormLabel>
            <FormControl>
              <Input {...field} placeholder='400, 413' />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
      <FormField
        control={form.control}
        name={`rules.${props.index}.pattern`}
        render={({ field }) => (
          <FormItem className='sm:col-span-2'>
            <FormLabel>{t('Error pattern')}</FormLabel>
            <FormControl>
              <Textarea {...field} rows={2} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )
}

function ErrorRulesEditor(props: { rules: ErrorRecognitionRule[] }) {
  const { t, i18n } = useTranslation()
  const updateOption = useUpdateOption()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState('rules')
  const [openId, setOpenId] = useState<string | null>(null)
  const defaults = useMemo(
    () => ({
      rules: props.rules.map((rule) => ({
        ...rule,
        status_codes: rule.status_codes?.join(', ') ?? '',
      })),
    }),
    [props.rules]
  )
  const schema = useMemo(() => errorRecognitionFormSchema(t), [t])
  const form = useForm<ErrorRecognitionFormValues>({
    resolver: zodResolver(schema),
    defaultValues: defaults,
  })
  useEffect(() => {
    form.reset(defaults)
  }, [defaults, form])
  const rules = useFieldArray({ control: form.control, name: 'rules' })
  const values = useWatch({ control: form.control, name: 'rules' })
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const isDirty = form.formState.isDirty
  const addRule = (pattern = '', statusCode?: number) => {
    const name = `rule_${nanoid()}`
    rules.append(
      {
        name,
        label: '',
        enabled: true,
        match: 'contains',
        pattern,
        status_codes: statusCode ? String(statusCode) : '',
      },
      { shouldFocus: false }
    )
    setOpenId(name)
    setTab('rules')
  }
  const save = () => {
    void form
      .handleSubmit(
        async (draft) => {
          await updateOption.mutateAsync({
            key: 'ErrorRecognitionRules',
            value: JSON.stringify(serializeErrorRecognitionRules(draft.rules)),
          })
          form.reset(draft)
          void queryClient.resetQueries({
            queryKey: ['error-recognition', 'inventory'],
          })
          void queryClient.invalidateQueries({ queryKey: ['logs'] })
        },
        (errors) => {
          const index = rules.fields.findIndex(
            (_, index) => errors.rules?.[index]
          )
          if (index >= 0) {
            setOpenId(rules.fields[index].id)
          }
          setTab('rules')
        }
      )()
      .catch(() => {
        /* The shared option mutation reports the failure and preserves the draft. */
      })
  }
  return (
    <Form {...form}>
      <SettingsPageFormActions
        onSave={save}
        onReset={() => form.reset(defaults)}
        isSaving={updateOption.isPending}
        isSaveDisabled={!isDirty}
        isResetDisabled={!isDirty}
      />
      <Tabs value={tab} onValueChange={setTab} className='min-w-0 gap-4'>
        <TabsList
          aria-label={t('Error recognition')}
          className='max-w-full overflow-x-auto'
        >
          <TabsTrigger value='rules'>{t('Rules')}</TabsTrigger>
          <TabsTrigger value='history'>{t('Historical errors')}</TabsTrigger>
          <TabsTrigger value='preview'>{t('Test match')}</TabsTrigger>
        </TabsList>
        <TabsContent value='rules'>
          <SettingsForm
            onSubmit={(event) => {
              event.preventDefault()
              save()
            }}
          >
            <p className='text-muted-foreground text-sm'>
              {t(
                'Rules are checked top to bottom. The first enabled match wins. Changes also apply to historical logs.'
              )}
            </p>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Regular expressions are validated and matched by the server using RE2.'
              )}
            </p>
            <fieldset
              disabled={updateOption.isPending}
              className='min-w-0 space-y-3'
            >
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={rules.fields.length >= 100}
                onClick={() => addRule()}
              >
                <Plus aria-hidden />
                {t('Add rule')}
              </Button>
              {rules.fields.length === 0 && (
                <p className='text-muted-foreground text-sm'>
                  {t('No error rules. Errors will use the general fallback.')}
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
                aria-label={t('Error rule priority')}
                values={rules.fields.map((rule) => rule.id)}
                onReorder={(ids) => {
                  if (updateOption.isPending) {
                    return
                  }
                  const order = rules.fields.map((rule) => rule.id)
                  for (let target = 0; target < ids.length; target++) {
                    const from = order.indexOf(ids[target])
                    if (from < 0 || from === target) {
                      continue
                    }
                    rules.move(from, target)
                    order.splice(target, 0, ...order.splice(from, 1))
                  }
                }}
                className='flex flex-col gap-2'
              >
                {rules.fields.map((rule, index) => {
                  const isOpen = openId === rule.id || openId === rule.name
                  const label = values[index]?.label
                    ? t(values[index].label)
                    : t('Error rule {{number}}', {
                        number: formatNumber(index + 1, locale),
                      })
                  return (
                    <AutoGroupOrderItem
                      key={rule.id}
                      group={rule.id}
                      label={label}
                      index={index}
                      count={rules.fields.length}
                      disabled={updateOption.isPending}
                      onMove={(from, direction) => {
                        const target = from + (direction === 'up' ? -1 : 1)
                        if (target >= 0 && target < rules.fields.length) {
                          rules.move(from, target)
                        }
                      }}
                      onRemove={() => rules.remove(index)}
                    >
                      <FormField
                        control={form.control}
                        name={`rules.${index}.enabled`}
                        render={({ field }) => (
                          <FormItem className='ml-auto'>
                            <FormControl>
                              <Switch
                                checked={field.value}
                                onCheckedChange={field.onChange}
                                aria-label={t('Enabled')}
                              />
                            </FormControl>
                          </FormItem>
                        )}
                      />
                      <Collapsible
                        open={isOpen}
                        onOpenChange={(open) =>
                          setOpenId(open ? rule.id : null)
                        }
                        className='w-full'
                      >
                        <CollapsibleTrigger
                          render={
                            <Button
                              type='button'
                              variant='ghost'
                              size='sm'
                              className='text-muted-foreground h-7 px-1'
                            />
                          }
                        >
                          <ChevronDown
                            aria-hidden
                            className={isOpen ? 'rotate-180' : undefined}
                          />
                          {t('Edit error rule')}
                        </CollapsibleTrigger>
                        <CollapsibleContent className='py-3'>
                          <ErrorRuleFields index={index} />
                        </CollapsibleContent>
                      </Collapsible>
                    </AutoGroupOrderItem>
                  )
                })}
              </Reorder.Group>
            </fieldset>
          </SettingsForm>
        </TabsContent>
        <TabsContent value='history'>
          <ErrorHistoryInventory
            onCreate={
              rules.fields.length < 100 && !updateOption.isPending
                ? addRule
                : undefined
            }
          />
        </TabsContent>
        <TabsContent value='preview'>
          <RulePreview rules={serializeErrorRecognitionRules(values)} />
        </TabsContent>
      </Tabs>
    </Form>
  )
}

export function ErrorRecognitionSection(props: { value: string }) {
  const { t } = useTranslation()
  const rules = useMemo(
    () => parseErrorRecognitionRules(props.value, t),
    [props.value, t]
  )
  return (
    <SettingsSection title={t('Error recognition')}>
      {rules === null ? (
        <>
          <SettingsPageFormActions onSave={() => {}} isSaveDisabled />
          <Alert variant='destructive'>
            <AlertDescription>
              {t(
                'Saved error rules could not be read. No data was changed. Correct the stored configuration before editing.'
              )}
            </AlertDescription>
          </Alert>
        </>
      ) : (
        <ErrorRulesEditor rules={rules} />
      )}
    </SettingsSection>
  )
}
