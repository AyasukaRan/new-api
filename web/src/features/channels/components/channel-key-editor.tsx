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
import { Reorder } from 'motion/react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AutoGroupOrderItem } from '@/components/auto-group-order-item'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { MultiSelect } from '@/components/multi-select'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toIntlLocale } from '@/i18n/languages'
import { formatCurrencyFromUSD } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import { getMultiKeyStatusConfig } from '../lib'
import type { ChannelKeyDraft } from '../lib/channel-form'
import type { Channel, ChannelTestKey } from '../types'
import { ChannelTestDialog } from './dialogs/channel-test-dialog'

type ChannelKeyEditorProps = {
  value: ChannelKeyDraft[]
  onChange: (value: ChannelKeyDraft[]) => void
  mode: 'random' | 'polling' | 'failover'
  onModeChange: (mode: 'random' | 'polling' | 'failover') => void
  channelGroups: string[]
  channelCost: number | null
  channel?: Channel
  disabled?: boolean
  sensitiveVisible?: boolean
  pendingKeys: string
  keyPlaceholder?: string
  onPendingKeysChange: (value: string) => void
}

function sortChannelKeysByCost(
  keys: ChannelKeyDraft[],
  channelCost: number | null
) {
  return [...keys].sort((a, b) => {
    const left =
      a.upstream_cost_ratio ?? channelCost ?? Number.POSITIVE_INFINITY
    const right =
      b.upstream_cost_ratio ?? channelCost ?? Number.POSITIVE_INFINITY
    return left === right ? 0 : left - right
  })
}

export function ChannelKeyEditor(props: ChannelKeyEditorProps) {
  const { t, i18n } = useTranslation()
  const { disabled, value, channelCost, onChange } = props
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const ratioFormatter = new Intl.NumberFormat(locale, {
    maximumSignificantDigits: 12,
  })
  const id = useId()
  const [removeIdentity, setRemoveIdentity] = useState<string | null>(null)
  const [manualOrder, setManualOrder] = useState(
    !!props.channel && props.mode === 'failover'
  )
  const [testKey, setTestKey] = useState<ChannelTestKey | null>(null)
  const identities = props.value.map(
    (key) => key.key_identity || key.draft_id || ''
  )
  const byIdentity = new Map(
    props.value.map((key, index) => [identities[index], key])
  )
  const failover = props.mode === 'failover'
  const modeOptions = [
    { value: 'failover', label: t('Key failover') },
    { value: 'random', label: t('Random') },
    { value: 'polling', label: t('Polling') },
  ]
  const reorder = (next: string[]) => {
    if (props.disabled || !failover) return
    setManualOrder(true)
    const reordered = next.flatMap((identity) => {
      const key = byIdentity.get(identity)
      return key ? [key] : []
    })
    if (reordered.length === props.value.length) {
      props.onChange(reordered)
    }
  }
  const move = (index: number, direction: 'up' | 'down') => {
    const target = index + (direction === 'up' ? -1 : 1)
    if (target < 0 || target >= identities.length) return
    const order = [...identities]
    ;[order[index], order[target]] = [order[target], order[index]]
    reorder(order)
  }
  const sortByCost = () => {
    setManualOrder(false)
    props.onChange(sortChannelKeysByCost(props.value, props.channelCost))
  }
  useEffect(() => {
    if (disabled || !failover || manualOrder) return
    const sorted = sortChannelKeysByCost(value, channelCost)
    if (sorted.some((key, index) => key !== value[index])) {
      onChange(sorted)
    }
  }, [disabled, value, channelCost, onChange, failover, manualOrder])
  const update = (index: number, patch: Partial<ChannelKeyDraft>) => {
    props.onChange(
      props.value.map((item, itemIndex) =>
        itemIndex === index ? { ...item, ...patch } : item
      )
    )
  }

  return (
    <section className='min-w-0 space-y-4' aria-label={t('Channel keys')}>
      <div className='flex flex-wrap items-end justify-between gap-3'>
        <Field className='min-w-40 flex-1'>
          <FieldLabel htmlFor={`${id}-mode`}>
            {t('Multi-Key Strategy')}
          </FieldLabel>
          <Select
            items={modeOptions}
            value={props.mode}
            disabled={props.disabled}
            onValueChange={(mode) => {
              if (
                mode !== 'random' &&
                mode !== 'polling' &&
                mode !== 'failover'
              ) {
                return
              }
              props.onModeChange(mode)
              if (mode === 'failover') sortByCost()
            }}
          >
            <SelectTrigger id={`${id}-mode`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {modeOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {failover && (
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={props.disabled || props.value.length < 2}
            onClick={sortByCost}
          >
            {t('Sort keys by cost')}
          </Button>
        )}
      </div>
      <FieldDescription>
        {failover
          ? t(
              'Use the first available key; try the next only after failure. Drag to set the fallback order, or sort from lowest to highest cost.'
            )
          : t(
              'Key order applies only to failover mode. Existing random and polling strategies remain unchanged.'
            )}
      </FieldDescription>
      <FieldDescription>
        {t(
          'Key changes take effect when you save the channel. Unconfigured costs are sorted last.'
        )}
      </FieldDescription>
      <Reorder.Group
        as='ol'
        axis='y'
        values={identities}
        onReorder={reorder}
        className='space-y-3'
        aria-label={t('Key fallback order')}
      >
        {props.value.map((key, index) => {
          const identity = identities[index]
          const label = t('Key #{{index}}', {
            index: key.key_index < 0 ? index + 1 : key.key_index + 1,
          })
          let statusCode = key.status
          if (key.enabled !== undefined) {
            statusCode = key.enabled ? 1 : 2
          }
          const status = getMultiKeyStatusConfig(statusCode)
          const balance = !props.channel?.balance_monitor?.configuration_changed
            ? props.channel?.balance_monitor?.key_balances.find(
                (item) => item.index === key.key_index
              )
            : undefined
          const effectiveCost = key.upstream_cost_ratio ?? props.channelCost
          let balanceLabel = t('Query failed')
          if (balance?.balance !== null && balance?.balance !== undefined) {
            balanceLabel = formatCurrencyFromUSD(balance.balance, {
              digitsLarge: 2,
              digitsSmall: 4,
              abbreviate: false,
            })
          }
          if (props.sensitiveVisible === false) {
            balanceLabel = '••••'
          }
          return (
            <AutoGroupOrderItem
              key={identity}
              group={identity}
              label={label}
              index={index}
              count={props.value.length}
              disabled={props.disabled}
              reorderDisabled={!failover}
              onMove={move}
              onRemove={
                props.value.length > 1
                  ? () => setRemoveIdentity(identity)
                  : undefined
              }
              leading={
                failover ? (
                  <span className='text-muted-foreground text-xs tabular-nums'>
                    {formatNumber(index + 1, locale)}
                  </span>
                ) : undefined
              }
            >
              <StatusBadge
                label={t(status.label)}
                variant={status.variant}
                copyable={false}
              />
              <div className='min-w-0 basis-full space-y-3'>
                {key.key_identity ? (
                  <div className='font-mono text-xs break-all'>
                    {props.sensitiveVisible === false
                      ? '••••'
                      : key.key_preview}
                  </div>
                ) : (
                  <Field>
                    <FieldLabel
                      className='sr-only'
                      htmlFor={`${id}-${identity}-key`}
                    >
                      {t('API key for {{key}}', { key: label })}
                    </FieldLabel>
                    <Input
                      id={`${id}-${identity}-key`}
                      type='password'
                      autoComplete='off'
                      spellCheck={false}
                      value={key.key || ''}
                      disabled={props.disabled}
                      onChange={(event) =>
                        update(index, { key: event.target.value })
                      }
                    />
                  </Field>
                )}
                <div className='grid gap-3 xl:grid-cols-2'>
                  <Field>
                    <FieldLabel htmlFor={`${id}-${identity}-groups`}>
                      {t('Group')}
                    </FieldLabel>
                    <MultiSelect
                      id={`${id}-${identity}-groups`}
                      options={props.channelGroups.map((group) => ({
                        value: group,
                        label: group,
                      }))}
                      selected={key.groups}
                      onChange={(groups) => update(index, { groups })}
                      disabled={props.disabled}
                      maxVisibleChips={2}
                      placeholder={t('All channel groups')}
                    />
                    <FieldDescription>
                      {key.groups.length === 0 ? t('All channel groups') : null}
                    </FieldDescription>
                  </Field>
                  <Field>
                    <FieldLabel htmlFor={`${id}-${identity}-cost`}>
                      {t('Upstream cost multiplier')}
                    </FieldLabel>
                    <Input
                      id={`${id}-${identity}-cost`}
                      type='number'
                      min={0}
                      max={10000}
                      step='any'
                      value={
                        Number.isNaN(key.upstream_cost_ratio)
                          ? ''
                          : (key.upstream_cost_ratio ?? '')
                      }
                      aria-invalid={
                        key.upstream_cost_ratio !== null &&
                        (!Number.isFinite(key.upstream_cost_ratio) ||
                          key.upstream_cost_ratio < 0 ||
                          key.upstream_cost_ratio > 10000)
                      }
                      placeholder={t('Inherit channel cost')}
                      disabled={props.disabled}
                      onChange={(event) =>
                        update(index, {
                          upstream_cost_ratio:
                            event.target.value === ''
                              ? null
                              : event.target.valueAsNumber,
                        })
                      }
                    />
                    {key.upstream_cost_ratio !== null &&
                      (!Number.isFinite(key.upstream_cost_ratio) ||
                        key.upstream_cost_ratio < 0 ||
                        key.upstream_cost_ratio > 10000) && (
                        <FieldError>
                          {t(
                            'Upstream cost multiplier must be between 0 and 10000'
                          )}
                        </FieldError>
                      )}
                    <FieldDescription>
                      {effectiveCost === null
                        ? t('Not configured')
                        : t('Effective cost: {{ratio}}×', {
                            ratio: ratioFormatter.format(effectiveCost),
                          })}
                    </FieldDescription>
                  </Field>
                </div>
                <div className='flex flex-wrap items-center gap-3'>
                  <Field className='w-auto flex-row items-center gap-2'>
                    <Switch
                      id={`${id}-${identity}-enabled`}
                      size='sm'
                      checked={key.enabled ?? key.status === 1}
                      disabled={props.disabled}
                      onCheckedChange={(enabled) => update(index, { enabled })}
                    />
                    <FieldLabel htmlFor={`${id}-${identity}-enabled`}>
                      {t('Enabled')}
                    </FieldLabel>
                  </Field>
                  {balance && (
                    <span className='text-muted-foreground text-xs'>
                      {t('Balance')}: {balanceLabel}
                    </span>
                  )}
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    disabled={
                      props.disabled || !key.key_identity || !props.channel
                    }
                    onClick={() =>
                      setTestKey({
                        key_index: key.key_index,
                        key_identity: key.key_identity,
                      })
                    }
                  >
                    {t('Test key')}
                  </Button>
                  {!key.key_identity && (
                    <span className='text-muted-foreground text-xs'>
                      {t('Save the channel before testing a new key.')}
                    </span>
                  )}
                </div>
              </div>
            </AutoGroupOrderItem>
          )
        })}
      </Reorder.Group>
      <Field>
        <FieldLabel htmlFor={`${id}-new-keys`} required={!props.value.length}>
          {props.value.length ? t('Add keys') : t('API Key')}
        </FieldLabel>
        <Textarea
          id={`${id}-new-keys`}
          rows={2}
          value={props.pendingKeys}
          onChange={(event) => props.onPendingKeysChange(event.target.value)}
          disabled={props.disabled}
          autoComplete='off'
          spellCheck={false}
          placeholder={props.keyPlaceholder ?? t('One key per line')}
        />
        <Button
          type='button'
          variant='outline'
          className='w-fit'
          disabled={props.disabled || !props.pendingKeys.trim()}
          onClick={() => {
            const added = props.pendingKeys
              .split(/\r?\n/)
              .map((key) => key.trim())
              .filter(Boolean)
              .map(
                (key): ChannelKeyDraft => ({
                  draft_id: crypto.randomUUID(),
                  key,
                  key_index: -1,
                  key_identity: '',
                  key_preview: '',
                  groups: [],
                  upstream_cost_ratio: null,
                  effective_upstream_cost_ratio: null,
                  status: 1,
                })
              )
            props.onChange([...props.value, ...added])
            props.onPendingKeysChange('')
          }}
        >
          {t('Add keys')}
        </Button>
      </Field>
      <ConfirmDialog
        open={removeIdentity !== null}
        onOpenChange={(open) => !open && setRemoveIdentity(null)}
        title={t('Remove key')}
        desc={t('This key will be removed when you save the channel.')}
        destructive
        confirmText={t('Remove')}
        disabled={props.disabled || props.value.length <= 1}
        handleConfirm={() => {
          if (props.disabled || props.value.length <= 1) return
          props.onChange(
            props.value.filter(
              (_key, index) => identities[index] !== removeIdentity
            )
          )
          setRemoveIdentity(null)
        }}
      />
      {testKey && props.channel && (
        <ChannelTestDialog
          open
          channel={props.channel}
          selectedKey={testKey}
          onOpenChange={(open) => !open && setTestKey(null)}
        />
      )}
    </section>
  )
}
