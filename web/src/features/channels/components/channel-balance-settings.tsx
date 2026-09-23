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
import { Link } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useWatch, type Control } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { BALANCE_QUERY_DEFAULT, BALANCE_QUERY_TYPES } from '../constants'
import type { ChannelFormValues } from '../lib/channel-form'

export function ChannelBalanceSettings(props: {
  control: Control<ChannelFormValues>
}) {
  const { t } = useTranslation()
  const disabled =
    useWatch({ control: props.control, name: 'balance_query_disabled' }) ===
    true
  const isRoot = useAuthStore(
    (state) => state.auth.user?.role === ROLE.SUPER_ADMIN
  )
  const balanceQueryItems = useMemo(
    () => [
      { value: BALANCE_QUERY_DEFAULT, label: t('Follow channel type') },
      ...BALANCE_QUERY_TYPES.map((entry) => ({
        value: entry.value,
        label: entry.value === 'custom' ? t('Custom') : entry.label,
      })),
    ],
    [t]
  )
  return (
    <section
      className='space-y-4 rounded-lg border p-4'
      aria-label={t('Balance queries')}
    >
      <FormField
        control={props.control}
        name='balance_query_disabled'
        render={({ field }) => (
          <FormItem className='flex items-center justify-between gap-4'>
            <div className='space-y-1'>
              <FormLabel>{t('Balance queries')}</FormLabel>
              <FormDescription>
                {t(
                  'Turning this off skips manual, bulk, and scheduled balance queries for this channel. Previously recorded balances are retained.'
                )}
              </FormDescription>
            </div>
            <FormControl>
              <Switch
                checked={!field.value}
                onCheckedChange={(checked) => field.onChange(!checked)}
              />
            </FormControl>
          </FormItem>
        )}
      />
      <fieldset disabled={disabled} className='space-y-4 disabled:opacity-60'>
        <FormField
          control={props.control}
          name='balance_query_type'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Balance Query API')}</FormLabel>
              <Select
                disabled={disabled}
                items={balanceQueryItems}
                value={field.value || BALANCE_QUERY_DEFAULT}
                onValueChange={(value) =>
                  field.onChange(
                    value === BALANCE_QUERY_DEFAULT ? '' : (value ?? '')
                  )
                }
              >
                <FormControl>
                  <SelectTrigger disabled={disabled}>
                    <SelectValue />
                  </SelectTrigger>
                </FormControl>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    {balanceQueryItems.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FormDescription>
                {t(
                  "Query this channel's balance through another provider's billing API, for a gateway that relays one protocol but bills through another."
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={props.control}
          name='balance_query_base_url'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Balance Query Address')}</FormLabel>
              <FormControl>
                <Input
                  disabled={disabled}
                  {...field}
                  value={field.value || ''}
                  placeholder='https://api.example.com'
                />
              </FormControl>
              <FormDescription>
                {t(
                  'Leave empty to use the channel address. Only the billing APIs addressed by base URL use this.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </fieldset>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Automatic balance refresh is managed in System Settings → Request policies → Channel health → Balance monitoring.'
        )}
      </p>
      {isRoot && (
        <Button
          variant='link'
          role='link'
          size='sm'
          className='h-auto p-0 text-xs'
          render={
            <Link
              to='/system-settings/request-policies/$section'
              params={{ section: 'health' }}
              target='_blank'
              rel='noopener noreferrer'
            />
          }
        >
          {t('Global balance refresh settings')}
        </Button>
      )}
    </section>
  )
}
