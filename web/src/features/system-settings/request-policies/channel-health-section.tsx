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
import { useMemo, useRef, useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { parseHttpStatusCodeRules } from '@/lib/http-status-code-rules'

import {
  SettingsControlChildren,
  SettingsControlGroup,
  SettingsForm,
  SettingsFormGrid,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { safeNumberFieldProps } from '../utils/numeric-field'
import { useSavePolicy } from './use-save-policy'

const MAX_CHANNEL_TEST_CONCURRENCY = 32

const createChannelHealthSchema = (
  t: (key: string, options?: Record<string, unknown>) => string
) =>
  z
    .object({
      AutomaticDisableChannelEnabled: z.boolean(),
      AutomaticDisableKeywords: z.string(),
      AutomaticDisableStatusCodes: z.string(),
      monitor_setting: z.object({
        auto_update_balance_enabled: z.boolean(),
        auto_update_balance_minutes: z.coerce
          .number()
          .min(1, t('Interval must be at least 1 minute'))
          .max(10080, t('Balance interval must not exceed 7 days')),
        auto_test_channel_enabled: z.boolean(),
        auto_test_channel_minutes: z.coerce
          .number()
          .int()
          .min(1, t('Interval must be at least 1 minute')),
        channel_test_concurrency: z.coerce
          .number()
          .int(t('Enter a positive integer'))
          .min(1, t('Channel test concurrency must be between 1 and 32'))
          .max(
            MAX_CHANNEL_TEST_CONCURRENCY,
            t('Channel test concurrency must be between 1 and 32')
          ),
      }),
    })
    .superRefine((values, ctx) => {
      const disableParsed = parseHttpStatusCodeRules(
        values.AutomaticDisableStatusCodes
      )
      if (!disableParsed.ok) {
        ctx.addIssue({
          code: 'custom',
          path: ['AutomaticDisableStatusCodes'],
          message: t('Invalid status code rules: {{tokens}}', {
            tokens: disableParsed.invalidTokens.join(', '),
          }),
        })
      }
    })

type ChannelHealthSchema = ReturnType<typeof createChannelHealthSchema>
type ChannelHealthFormValues = z.output<ChannelHealthSchema>
type ChannelHealthFormInput = z.input<ChannelHealthSchema>

type ChannelHealthSectionProps = {
  defaultValues: {
    AutomaticDisableChannelEnabled: boolean
    AutomaticDisableKeywords: string
    AutomaticDisableStatusCodes: string
    'monitor_setting.auto_update_balance_enabled': boolean
    'monitor_setting.auto_update_balance_minutes': number
    'monitor_setting.auto_test_channel_enabled': boolean
    'monitor_setting.auto_test_channel_minutes': number
    'monitor_setting.channel_test_concurrency': number
  }
}

function normalizeLineEndings(value: string) {
  return value.replaceAll('\r\n', '\n')
}

type NormalizedChannelHealthValues = {
  AutomaticDisableChannelEnabled: boolean
  AutomaticDisableKeywords: string
  AutomaticDisableStatusCodes: string
  'monitor_setting.auto_update_balance_enabled': boolean
  'monitor_setting.auto_update_balance_minutes': number
  'monitor_setting.auto_test_channel_enabled': boolean
  'monitor_setting.auto_test_channel_minutes': number
  'monitor_setting.channel_test_concurrency': number
}

const buildFormDefaults = (
  defaults: ChannelHealthSectionProps['defaultValues']
): ChannelHealthFormInput => ({
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: defaults.AutomaticDisableStatusCodes ?? '',
  monitor_setting: {
    auto_update_balance_enabled:
      defaults['monitor_setting.auto_update_balance_enabled'] ?? false,
    auto_update_balance_minutes:
      defaults['monitor_setting.auto_update_balance_minutes'] ?? 60,
    auto_test_channel_enabled:
      defaults['monitor_setting.auto_test_channel_enabled'],
    auto_test_channel_minutes:
      defaults['monitor_setting.auto_test_channel_minutes'],
    channel_test_concurrency:
      defaults['monitor_setting.channel_test_concurrency'],
  },
})

const normalizeDefaults = (
  defaults: ChannelHealthSectionProps['defaultValues']
): NormalizedChannelHealthValues => ({
  AutomaticDisableChannelEnabled: defaults.AutomaticDisableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    defaults.AutomaticDisableKeywords ?? ''
  ),
  AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
    defaults.AutomaticDisableStatusCodes ?? ''
  ).normalized,
  'monitor_setting.auto_update_balance_enabled':
    defaults['monitor_setting.auto_update_balance_enabled'] ?? false,
  'monitor_setting.auto_update_balance_minutes':
    defaults['monitor_setting.auto_update_balance_minutes'] ?? 60,
  'monitor_setting.auto_test_channel_enabled':
    defaults['monitor_setting.auto_test_channel_enabled'],
  'monitor_setting.auto_test_channel_minutes':
    defaults['monitor_setting.auto_test_channel_minutes'],
  'monitor_setting.channel_test_concurrency':
    defaults['monitor_setting.channel_test_concurrency'],
})

const normalizeFormValues = (
  values: ChannelHealthFormValues
): NormalizedChannelHealthValues => ({
  AutomaticDisableChannelEnabled: values.AutomaticDisableChannelEnabled,
  AutomaticDisableKeywords: normalizeLineEndings(
    values.AutomaticDisableKeywords
  ),
  AutomaticDisableStatusCodes: parseHttpStatusCodeRules(
    values.AutomaticDisableStatusCodes
  ).normalized,
  'monitor_setting.auto_update_balance_enabled':
    values.monitor_setting.auto_update_balance_enabled,
  'monitor_setting.auto_update_balance_minutes':
    values.monitor_setting.auto_update_balance_minutes,
  'monitor_setting.auto_test_channel_enabled':
    values.monitor_setting.auto_test_channel_enabled,
  'monitor_setting.auto_test_channel_minutes':
    values.monitor_setting.auto_test_channel_minutes,
  'monitor_setting.channel_test_concurrency':
    values.monitor_setting.channel_test_concurrency,
})

export function ChannelHealthSection({
  defaultValues,
}: ChannelHealthSectionProps) {
  const { t } = useTranslation()
  const updateOption = useSavePolicy()
  const channelHealthSchema = createChannelHealthSchema(t)
  const baselineRef = useRef<NormalizedChannelHealthValues>(
    normalizeDefaults(defaultValues)
  )

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<
    ChannelHealthFormInput,
    unknown,
    ChannelHealthFormValues
  >({
    resolver: zodResolver(channelHealthSchema),
    defaultValues: formDefaults,
  })

  useResetForm(form, formDefaults)
  useEffect(() => {
    baselineRef.current = normalizeDefaults(defaultValues)
  }, [defaultValues])

  const autoDisableStatusCodes = form.watch('AutomaticDisableStatusCodes')
  const autoDisableParsed = useMemo(
    () => parseHttpStatusCodeRules(autoDisableStatusCodes),
    [autoDisableStatusCodes]
  )

  const onSubmit = async (values: ChannelHealthFormValues) => {
    const normalized = normalizeFormValues(values)
    const updates = (
      Object.keys(normalized) as Array<keyof NormalizedChannelHealthValues>
    ).filter((key) => normalized[key] !== baselineRef.current[key])

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    try {
      await updateOption.mutateAsync(
        Object.fromEntries(updates.map((key) => [key, String(normalized[key])]))
      )
      baselineRef.current = normalized
    } catch {
      // Keep the edited values when the policy update fails.
    }
  }

  return (
    <SettingsSection title={t('Channel health')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={form.formState.isSubmitting}
          />
          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>
              {t('Channel health checks')}
            </h4>
            <SettingsFormGrid>
              <SettingsControlGroup>
                <FormField
                  control={form.control}
                  name='monitor_setting.auto_test_channel_enabled'
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>{t('Scheduled channel tests')}</FormLabel>
                        <FormDescription>
                          {t(
                            'Test idle models on each channel, excluding manually disabled channels. Results update monitoring only.'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />
                <SettingsControlChildren
                  role='group'
                  aria-label={t('Scheduled test options')}
                >
                  <FormField
                    control={form.control}
                    name='monitor_setting.auto_test_channel_minutes'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>
                          {t('Idle test interval (minutes)')}
                        </FormLabel>
                        <FormControl>
                          <Input
                            type='number'
                            min={1}
                            step={1}
                            {...safeNumberFieldProps(field)}
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Successful and failed real calls restart the timer for that channel and model only. Automatic tests wait for the full idle interval; manual tests run immediately.'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                </SettingsControlChildren>
              </SettingsControlGroup>
              <FormField
                control={form.control}
                name='monitor_setting.channel_test_concurrency'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Channel test concurrency')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={MAX_CHANNEL_TEST_CONCURRENCY}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Maximum number of channels tested at the same time (1-32)'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </div>
          <Separator />
          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>{t('Balance monitoring')}</h4>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='monitor_setting.auto_update_balance_enabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>
                        {t('Automatically refresh balances')}
                      </FormLabel>
                      <FormDescription>
                        {t(
                          'Refresh accounts only for channels with balance queries enabled, and retain balance and usage history. Channel status is unchanged.'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
              <FormField
                control={form.control}
                name='monitor_setting.auto_update_balance_minutes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>
                      {t('Balance refresh interval (minutes)')}
                    </FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={1}
                        max={10080}
                        step={1}
                        {...safeNumberFieldProps(field)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Only providers with a balance API can report an account balance.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </div>
          <Separator />
          <div className='flex min-w-0 flex-col gap-4'>
            <h4 className='text-sm font-medium'>{t('Auto-disable rules')}</h4>
            <SettingsFormGrid>
              <FormField
                control={form.control}
                name='AutomaticDisableChannelEnabled'
                render={({ field }) => (
                  <SettingsSwitchItem>
                    <SettingsSwitchContent>
                      <FormLabel>{t('Disable on failure')}</FormLabel>
                      <FormDescription>
                        {t(
                          'Automatically disable channels when relay requests match the failure rules'
                        )}
                      </FormDescription>
                    </SettingsSwitchContent>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                      />
                    </FormControl>
                  </SettingsSwitchItem>
                )}
              />
              <FormField
                control={form.control}
                name='AutomaticDisableStatusCodes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Auto-disable status codes')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('e.g. 401, 403, 429, 500-599')}
                        value={field.value}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Accepts comma-separated status codes and inclusive ranges.'
                      )}{' '}
                      {autoDisableParsed.ok &&
                        autoDisableParsed.normalized &&
                        autoDisableParsed.normalized !== field.value.trim() && (
                          <span className='text-muted-foreground'>
                            {t('Normalized:')} {autoDisableParsed.normalized}
                          </span>
                        )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='AutomaticDisableKeywords'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Failure keywords')}</FormLabel>
                    <FormControl>
                      <Textarea
                        rows={6}
                        placeholder={t('one keyword per line')}
                        {...field}
                        onChange={(event) => field.onChange(event.target.value)}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'If an upstream error contains any of these keywords (case insensitive), the channel will be disabled automatically.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </SettingsFormGrid>
          </div>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
