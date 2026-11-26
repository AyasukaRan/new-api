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
import type { ColumnDef, HeaderContext } from '@tanstack/react-table'
import { FileSearch, GitBranch, KeyRound, Sparkles } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Action } from '@/components/ai-elements/actions'
import { CopyButton } from '@/components/copy-button'
import { TruncatedCell } from '@/components/data-table'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'
import {
  normalizeTierLabel,
  parseTaskTiersFromExpr,
} from '@/features/pricing/lib/billing-expr'
import {
  formatTaskUsageUnitPrice,
  getTaskUsagePriceUnitLabelKey,
} from '@/features/pricing/lib/dynamic-price'
import { pluginUsageSchema } from '@/features/pricing/lib/plugin-pricing'
import { taskUsageUnitLabel } from '@/features/pricing/lib/task-price-display'
import type { BillingUsageSchema } from '@/features/pricing/types'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import {
  formatLogQuota,
  formatNumber,
  formatTimestampToDate,
} from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { LOG_TYPE_ALL_VALUE, LOG_TYPE_ENUM } from '../../constants'
import type { UsageLog } from '../../data/schema'
import type { LogFilterColumn } from '../../lib/filter'
import {
  canShowLogCost,
  formatModelName,
  decodeBillingExprB64,
  getTieredBillingSummary,
  hasAnyCacheTokens,
  getLogMetadata,
  isViolationFeeLog,
  renderAuditContent,
  renderLogContent,
  getRetryDecisionReason,
  isFailedStreamResponse,
} from '../../lib/format'
import { summarizeRequestFailure } from '../../lib/request-failure-summary'
import {
  isDisplayableLogType,
  isTimingLogType,
  isConsumeLogType,
  getLogTypeConfig,
  isPerCallBilling,
} from '../../lib/utils'
import type {
  LogOtherData,
  LogSource,
  UsageLogDetailSelection,
} from '../../types'
import { BillingTierIcon, type BillingTier } from '../billing-tier-icon'
import { LogCostDisplay } from '../log-cost-display'
import { LogHeaderFilter } from '../log-header-filter'
import { LogAccountCell, LogSourceCell } from '../log-identity-cells'
import { LogFilterValue, useLogValueFilter } from '../log-value-filter'
import { ModelBadge } from '../model-badge'
import { RequestMetadataTags } from '../request-metadata-tags'
import { RequestPluginLogMarker } from '../request-plugin-record'
import { StreamModeCell } from '../timing-metrics-cell'
import { TokenPerformanceCell } from '../token-performance-cell'
import { useUsageLogsContext } from '../usage-logs-provider'

interface DetailSegment {
  text: string
  period?: BillingTier
  muted?: boolean
  danger?: boolean
}

function tierDetailSegment(label: string, priceText: string): DetailSegment {
  const period = normalizeTierLabel(label)
  if (period === 'peak' || period === 'off_peak') {
    return { text: priceText, period }
  }
  if (period === 'standard' || period === 'base') {
    return { text: priceText, period: 'standard' }
  }
  return { text: `${label} · ${priceText}` }
}

function formatRatioCompact(ratio: number | undefined): string {
  if (ratio == null || !Number.isFinite(ratio)) return '-'
  return ratio % 1 === 0
    ? String(ratio)
    : ratio.toFixed(4).replace(/\.?0+$/, '')
}

function buildDetailSegments(
  log: UsageLog,
  other: LogOtherData | null,
  t: (key: string, opts?: Record<string, unknown>) => string,
  isAdmin: boolean,
  language: string,
  usageSchema?: BillingUsageSchema
): DetailSegment[] {
  const segments = buildTypeDetailSegments(log, other, t, language, usageSchema)
  const adminSegments: DetailSegment[] = []
  // Quota saturation is a rare, admin-only anomaly marker; surface it first
  // and in danger styling so it stands out on the related billing log. The
  // backend already strips admin_info for non-admins; gate on isAdmin too as
  // defense in depth so the marker never leaks if that changes.
  if (isAdmin && other?.admin_info?.quota_saturation) {
    adminSegments.push({ text: t('Quota clamped'), danger: true })
  }
  return [...adminSegments, ...segments]
}

function buildTypeDetailSegments(
  log: UsageLog,
  other: LogOtherData | null,
  t: (key: string, opts?: Record<string, unknown>) => string,
  language: string,
  usageSchema?: BillingUsageSchema
): DetailSegment[] {
  // Top-up, audit, and login logs can carry a localized operation descriptor.
  if (log.type === 1 || log.type === 3 || log.type === 7) {
    const text = renderAuditContent(other, t) ?? renderLogContent(other, t)
    return text ? [{ text }] : []
  }

  if (log.type === 6) {
    return [{ text: t('Async task refund') }]
  }

  if (!isConsumeLogType(log.type)) return []

  const isViolation = isViolationFeeLog(other)
  if (isViolation) {
    const segments: DetailSegment[] = []
    segments.push({ text: t('Violation Fee'), danger: true })
    if (other?.violation_fee_code) {
      segments.push({
        text: other.violation_fee_code,
        muted: true,
      })
    }
    segments.push({
      text: `${t('Fee')}: ${formatLogQuota(other?.fee_quota ?? log.quota)}`,
      muted: true,
    })
    return segments
  }

  if (!other) return []

  const segments: DetailSegment[] = []

  const priceOpts = {
    digitsLarge: 4,
    digitsSmall: other.metered_usage ? 8 : 6,
    abbreviate: false,
  }
  const formatPrice = (price: number) =>
    `${formatBillingCurrencyFromUSD(price, priceOpts)}/M`
  const formatPriceCompact = (price: number) =>
    formatBillingCurrencyFromUSD(price, priceOpts)
  const formatPriceList = (prices: string[], showUnit: boolean) => {
    const text = prices.join(' / ')
    return showUnit ? `${text}/M` : text
  }
  const isTieredExpr = other.billing_mode === 'tiered_expr'
  const tieredSummary = getTieredBillingSummary(other, log.model_name)
  if (isTieredExpr && other.is_task) {
    const tiers = parseTaskTiersFromExpr(
      decodeBillingExprB64(other.expr_b64),
      usageSchema,
      true
    )
    const tier = tiers.find(
      (entry) =>
        Boolean(other.matched_tier) &&
        normalizeTierLabel(entry.label) ===
          normalizeTierLabel(other.matched_tier)
    )
    if (tier) {
      const prices = Object.entries(tier.unitPrices).map(([field, price]) => {
        const definition = usageSchema?.[field]
        const unitKey = getTaskUsagePriceUnitLabelKey(definition?.unit)
        const unitLabel = taskUsageUnitLabel(definition, language, t(unitKey))
        return `${field} ${formatTaskUsageUnitPrice(price, { tokenUnit: 'M' })}/${unitLabel}`
      })
      if (tier.constant > 0) {
        prices.push(
          `${t('Additional charge')} ${formatTaskUsageUnitPrice(tier.constant, { tokenUnit: 'M' })}/${t('request')}`
        )
      }
      segments.push(
        tierDetailSegment(tier.label || t('Default'), prices.join(' · '))
      )
    } else {
      segments.push({
        text: `${t('Dynamic Pricing')} · ${other.matched_tier || t('Special billing expression')}`,
        muted: true,
      })
    }
  } else if (isTieredExpr) {
    if (tieredSummary) {
      const baseEntries = tieredSummary.priceEntries
        .filter((entry) => ['inputPrice', 'outputPrice'].includes(entry.field))
        .map((entry) => formatPriceCompact(entry.price))
      if (baseEntries.length > 0) {
        const tierLabel = tieredSummary.tier.label || t('Default')
        segments.push(
          tierDetailSegment(tierLabel, formatPriceList(baseEntries, true))
        )
      }

      const cacheEntries = tieredSummary.priceEntries
        .filter((entry) =>
          [
            'cacheReadPrice',
            'cacheCreatePrice',
            'cacheCreate1hPrice',
            'cacheCreate12hPrice',
          ].includes(entry.field)
        )
        .map((entry) => {
          return `${t(entry.shortLabel)} ${formatPrice(entry.price)}`
        })
      if (cacheEntries.length > 0) {
        segments.push({
          text: cacheEntries.join(' · '),
          muted: true,
        })
      }

      const otherEntries = tieredSummary.priceEntries
        .filter(
          (entry) =>
            ![
              'inputPrice',
              'outputPrice',
              'cacheReadPrice',
              'cacheCreatePrice',
              'cacheCreate1hPrice',
              'cacheCreate12hPrice',
            ].includes(entry.field)
        )
        .map((entry): DetailSegment =>
          entry.unit
            ? tierDetailSegment(
                tieredSummary.tier.label || t('Default'),
                `${t(entry.shortLabel)} ${formatPriceCompact(entry.price)}/${t(entry.unit)}`
              )
            : { text: `${t(entry.shortLabel)} ${formatPrice(entry.price)}` }
        )
      if (otherEntries.length > 0) {
        segments.push({
          text: otherEntries.map((entry) => entry.text).join(' · '),
          period: otherEntries.find((entry) => entry.period)?.period,
          muted: true,
        })
      }
    } else {
      segments.push({
        text: `${t('Dynamic Pricing')} · ${other.matched_tier || t('Special billing expression')}`,
        muted: true,
      })
    }
  } else {
    const modelPrice = other.model_price
    const isPerCall = isPerCallBilling(modelPrice)
    if (isPerCall && modelPrice != null) {
      segments.push({
        text: `${t('Per-call')} · ${formatBillingCurrencyFromUSD(modelPrice, priceOpts)}`,
      })
    } else if (other.model_ratio != null) {
      const inputPriceUSD = other.model_ratio * 2.0
      const baseEntries = [formatPriceCompact(inputPriceUSD)]
      if (other.completion_ratio != null) {
        baseEntries.push(
          formatPriceCompact(inputPriceUSD * other.completion_ratio)
        )
      }
      segments.push(
        tierDetailSegment('standard', formatPriceList(baseEntries, true))
      )

      if (hasAnyCacheTokens(other)) {
        const cacheEntries = [
          other.cache_ratio != null && other.cache_ratio !== 1
            ? formatPriceCompact(inputPriceUSD * other.cache_ratio)
            : null,
          other.cache_creation_ratio != null && other.cache_creation_ratio !== 1
            ? formatPriceCompact(inputPriceUSD * other.cache_creation_ratio)
            : null,
          other.cache_creation_ratio_1h != null &&
          other.cache_creation_ratio_1h !== 0
            ? formatPriceCompact(inputPriceUSD * other.cache_creation_ratio_1h)
            : null,
          other.cache_creation_ratio_12h != null &&
          other.cache_creation_ratio_12h !== 0
            ? formatPriceCompact(inputPriceUSD * other.cache_creation_ratio_12h)
            : null,
        ].filter(Boolean) as string[]

        if (cacheEntries.length > 0) {
          segments.push({
            text: `${t('Cache')} ${formatPriceList(cacheEntries, false)}`,
            muted: true,
          })
        }
      }
    } else {
      const userGroupRatio = other.user_group_ratio
      const groupRatio = other.group_ratio
      const isUserGroup =
        userGroupRatio != null &&
        Number.isFinite(userGroupRatio) &&
        userGroupRatio !== -1
      const effectiveRatio = isUserGroup ? userGroupRatio : groupRatio
      const ratioLabel = isUserGroup
        ? t('User Exclusive Ratio')
        : t('Group Ratio')

      if (effectiveRatio != null && Number.isFinite(effectiveRatio)) {
        segments.push({
          text: `${ratioLabel} ${formatRatioCompact(effectiveRatio)}x`,
        })
      }
    }
  }

  if (other.is_system_prompt_overwritten) {
    segments.push({
      text: t('System Prompt Override'),
      danger: true,
    })
  }

  return segments
}

function hasLogEventDetail(log: UsageLog, isAdmin: boolean): boolean {
  if (!isConsumeLogType(log.type)) return true
  const other = getLogMetadata(log)
  return Boolean(
    isFailedStreamResponse(other?.stream_status) ||
    other?.retry_decision ||
    other?.is_system_prompt_overwritten ||
    isViolationFeeLog(other) ||
    (isAdmin && other?.admin_info?.quota_saturation) ||
    other?.content_parts?.length ||
    log.content?.trim()
  )
}

const filterColumns: Partial<Record<string, LogFilterColumn>> = {
  is_stream: 'transport',
  channel: 'channel',
  username: 'user',
  token_name: 'token',
  group: 'group',
  model_name: 'model',
  reasoning_effort: 'reasoning',
  request_source: 'source',
}

export function useCommonLogsColumns(
  isAdmin: boolean,
  onShowDetails: (selection: UsageLogDetailSelection) => void,
  source: LogSource = 'usage',
  showBillingSource = false,
  compact = false,
  inlineEvents = false,
  headerFilters = false
): ColumnDef<UsageLog>[] {
  const { t } = useTranslation()
  const currency = useSystemConfigStore((state) => state.config.currency)
  return useMemo(() => {
    const columns: ColumnDef<UsageLog>[] = [
      {
        accessorKey: 'created_at',
        header: t('Time'),
        cell: ({ row }) => {
          const log = row.original
          const timestamp = row.getValue('created_at') as number
          const config = getLogTypeConfig(log.type, source)
          const dateTime = formatTimestampToDate(timestamp)
          const [date, time] = dateTime.split(' ')
          const retryCount = log.retry_count ?? 0

          return (
            <div
              className='grid w-fit gap-1 whitespace-nowrap'
              title={dateTime}
            >
              {time ? (
                <time
                  dateTime={`${date}T${time}`}
                  className='font-mono text-[13px] leading-4 font-medium tabular-nums'
                >
                  {time}
                </time>
              ) : (
                <span>{date}</span>
              )}
              <div className='text-muted-foreground flex items-center gap-2 text-[11px] leading-4 font-normal'>
                {time && <span className='tabular-nums'>{date}</span>}
                <StatusBadge
                  label={t(config.label)}
                  variant={config.color as StatusBadgeProps['variant']}
                  type='text'
                  showDot
                  copyable={false}
                  className='text-muted-foreground gap-1 text-[11px] font-normal'
                />
                {retryCount > 0 && (
                  <Tooltip>
                    <TooltipTrigger
                      render={
                        <Button
                          variant='ghost'
                          size='sm'
                          className='text-muted-foreground h-4 gap-1 rounded-sm px-0.5 text-[11px] tabular-nums'
                          aria-label={t('Attempts: {{count}}', {
                            count: retryCount + 1,
                          })}
                          onClick={() =>
                            onShowDetails({ log, initialView: 'overview' })
                          }
                        />
                      }
                    >
                      <GitBranch className='size-3' aria-hidden='true' />
                      {formatNumber(retryCount)}
                    </TooltipTrigger>
                    <TooltipContent>
                      {t('Retry Chain')} ·{' '}
                      {t('Attempts: {{count}}', { count: retryCount + 1 })}
                    </TooltipContent>
                  </Tooltip>
                )}
              </div>
            </div>
          )
        },
        filterFn: (row, _id, value) => {
          if (!Array.isArray(value) || value.length === 0) return true
          if (value.includes(LOG_TYPE_ALL_VALUE)) return true
          return value.includes(String(row.original.type))
        },
        enableHiding: false,
        size: 180,
      },
    ]

    if (isAdmin) {
      columns.push({
        id: 'channel',
        header: t('Channel'),
        accessorFn: (row) => row.channel,
        cell: function ChannelCell({ row }) {
          const { sensitiveVisible, setAffinityTarget, setAffinityDialogOpen } =
            useUsageLogsContext()
          const log = row.original
          const filter = useLogValueFilter(
            'channel',
            sensitiveVisible &&
              isDisplayableLogType(log.type) &&
              log.channel > 0
              ? String(log.channel)
              : undefined,
            log.channel_name
          )

          if (!isDisplayableLogType(log.type)) return null

          const other = getLogMetadata(log)
          const affinity = other?.admin_info?.channel_affinity
          const rawUseChannel = other?.admin_info?.use_channel ?? []
          const useChannel = Array.isArray(rawUseChannel)
            ? rawUseChannel.map(String).filter(Boolean)
            : []
          const hasRetryChain = useChannel.length > 1
          const channelChain = hasRetryChain
            ? useChannel.join(' → ')
            : undefined
          const channelDisplay = log.channel_name
            ? `${log.channel_name} #${log.channel}`
            : `#${log.channel}`
          const channelIdDisplay = `#${log.channel}`
          const channelName = sensitiveVisible ? log.channel_name : '••••'
          const multiKeyIndex = other?.admin_info?.multi_key_index
          const showMultiKeyIndex =
            other?.admin_info?.is_multi_key === true &&
            typeof multiKeyIndex === 'number' &&
            Number.isFinite(multiKeyIndex)

          return (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <div className='flex max-w-full min-w-0 flex-col gap-0.5' />
                  }
                >
                  <div className='group/log-channel inline-flex w-fit max-w-full items-center gap-1.5'>
                    <LogFilterValue
                      field='channel'
                      value={filter ? String(log.channel) : undefined}
                      label={log.channel_name}
                    >
                      <StatusBadge
                        label={channelIdDisplay}
                        type='text'
                        copyText={String(log.channel)}
                        copyable={!filter}
                        size='sm'
                        showDot={false}
                        className='text-foreground dark:text-foreground font-mono text-xs font-semibold'
                      />
                    </LogFilterValue>
                    {showMultiKeyIndex && (
                      <StatusBadge
                        label={String(multiKeyIndex)}
                        size='sm'
                        showDot={false}
                        copyable={false}
                        variant='neutral'
                        className='border-border/70 text-muted-foreground h-4 gap-1 rounded border px-1 font-mono text-[10px]'
                        icon={KeyRound}
                        aria-label={`${t('Key')} ${multiKeyIndex}`}
                      />
                    )}
                    {hasRetryChain && (
                      <Popover>
                        <PopoverTrigger
                          render={
                            <button
                              type='button'
                              className='text-muted-foreground hover:text-foreground focus-visible:ring-ring inline-flex size-5 shrink-0 items-center justify-center rounded-full transition-colors focus-visible:ring-2 focus-visible:outline-none'
                              aria-label={t('Retry Chain')}
                              onClick={(e) => e.stopPropagation()}
                            />
                          }
                        >
                          <GitBranch
                            className='size-3.5 text-amber-500'
                            aria-hidden='true'
                          />
                        </PopoverTrigger>
                        <PopoverContent
                          side='top'
                          align='start'
                          className='w-64 text-xs'
                        >
                          <div className='flex flex-col gap-1'>
                            <p className='font-medium'>{t('Retry Chain')}</p>
                            <p className='text-muted-foreground font-mono break-all'>
                              {channelChain}
                            </p>
                          </div>
                        </PopoverContent>
                      </Popover>
                    )}
                    {affinity && (
                      <button
                        type='button'
                        aria-label={t('Channel Affinity')}
                        className='text-muted-foreground hover:text-foreground inline-flex size-4 shrink-0 items-center justify-center'
                        onClick={(e) => {
                          e.stopPropagation()
                          setAffinityTarget({
                            rule_name: affinity.rule_name || '',
                            using_group:
                              affinity.using_group ||
                              affinity.selected_group ||
                              '',
                            key_hint: affinity.key_hint || '',
                            key_fp: affinity.key_fp || '',
                          })
                          setAffinityDialogOpen(true)
                        }}
                      >
                        <Sparkles className='size-3' />
                      </button>
                    )}
                    {filter && (
                      <CopyButton
                        value={String(log.channel)}
                        aria-label={t('Copy {{field}}', {
                          field: t('Channel'),
                        })}
                        className='size-4 opacity-0 group-hover/log-channel:opacity-100 focus-visible:opacity-100'
                        iconClassName='size-3'
                      />
                    )}
                  </div>
                  {log.channel_name && (
                    <LogFilterValue
                      field='channel'
                      value={filter ? String(log.channel) : undefined}
                      label={log.channel_name}
                    >
                      <span className='text-muted-foreground truncate text-[11px]'>
                        {channelName}
                      </span>
                    </LogFilterValue>
                  )}
                </TooltipTrigger>
                <TooltipContent>
                  <div className='space-y-1'>
                    <p>
                      {sensitiveVisible ? channelDisplay : channelIdDisplay}
                    </p>
                    {channelChain && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Chain')}: {channelChain}
                      </p>
                    )}
                    {showMultiKeyIndex && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Key')}: {multiKeyIndex}
                      </p>
                    )}
                    {affinity && (
                      <div className='border-t pt-1 text-xs'>
                        <p className='font-medium'>{t('Channel Affinity')}</p>
                        <p>
                          {t('Rule')}: {affinity.rule_name || '-'}
                        </p>
                        <p>
                          {t('Group')}:{' '}
                          {sensitiveVisible
                            ? affinity.using_group ||
                              affinity.selected_group ||
                              '-'
                            : '••••'}
                        </p>
                      </div>
                    )}
                  </div>
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )
        },
      })
    }

    if (source === 'usage' && isAdmin) {
      columns.push({
        accessorKey: 'username',
        header: t('User'),
        cell: ({ row }) => (
          <LogAccountCell log={row.original} isAdmin field='user' />
        ),
        size: 120,
      })
    }
    columns.push({
      accessorKey: 'token_name',
      header: source !== 'usage' && isAdmin ? t('User / Token') : t('Token'),
      cell: ({ row }) => (
        <LogAccountCell
          log={row.original}
          isAdmin={isAdmin}
          field={source === 'usage' ? 'token' : undefined}
        />
      ),
      size: source === 'usage' ? 120 : 155,
    })
    if (source === 'usage') {
      columns.push({
        accessorKey: 'group',
        header: t('Group'),
        cell: ({ row }) => (
          <LogAccountCell log={row.original} isAdmin={isAdmin} field='group' />
        ),
        size: 160,
      })
    }
    columns.push({
      accessorKey: 'model_name',
      header: t('Model'),
      cell: ({ row, table }) => {
        if (!isDisplayableLogType(row.original.type)) return null
        const model = formatModelName(row.original)
        const requestPlugins = getLogMetadata(row.original)?.request_plugins
        return (
          <div className='flex w-max max-w-44 min-w-0 flex-col items-start gap-1'>
            <ModelBadge
              wrapText
              modelName={model.name}
              filterValue={row.original.model_name}
              actualModel={model.actualModel}
              responseModel={model.responseModel}
            />
            {requestPlugins?.length ? (
              <RequestPluginLogMarker items={requestPlugins} />
            ) : null}
            {compact && (
              <div className='flex w-full max-w-full flex-wrap items-center gap-1 [contain:inline-size] empty:hidden'>
                {table.getState().columnVisibility.reasoning_effort !==
                  false && (
                  <RequestMetadataTags
                    field='reasoning_effort'
                    isAdmin={isAdmin}
                    metadata={getLogMetadata(row.original)}
                  />
                )}
                {isAdmin &&
                  table.getState().columnVisibility.invoked_tools !== false && (
                    <RequestMetadataTags
                      field='invoked_tools'
                      isAdmin
                      metadata={getLogMetadata(row.original)}
                    />
                  )}
              </div>
            )}
          </div>
        )
      },
      meta: { mobileTitle: true },
    })
    columns.push({
      id: 'reasoning_effort',
      header: t('Reasoning Effort'),
      accessorFn: (log) => getLogMetadata(log)?.reasoning_effort,
      cell: ({ row }) => {
        const metadata = getLogMetadata(row.original)
        return (
          isDisplayableLogType(row.original.type) && (
            <div className='flex w-full min-w-0 justify-center'>
              {metadata?.reasoning_effort ? (
                <RequestMetadataTags
                  field='reasoning_effort'
                  isAdmin={isAdmin}
                  metadata={metadata}
                />
              ) : (
                <span className='text-muted-foreground/65'>—</span>
              )}
            </div>
          )
        )
      },
    })
    columns.push({
      id: 'request_source',
      accessorFn: (log) => log.ip,
      header: t('Request source'),
      meta: {
        displayFields: [
          ...(isAdmin
            ? [
                { id: 'client_tool', label: t('Client') },
                { id: 'user_agent', label: t('User Agent') },
              ]
            : []),
          { id: 'ip', label: t('IP Address') },
        ],
      },
      cell: ({ row, table }) => (
        <LogSourceCell
          log={row.original}
          isAdmin={isAdmin}
          visibility={table.getState().columnVisibility}
        />
      ),
    })
    if (isAdmin) {
      columns.push({
        id: 'invoked_tools',
        header: t('Tools'),
        accessorFn: (log) => getLogMetadata(log)?.invoked_tools,
        cell: ({ row }) =>
          isDisplayableLogType(row.original.type) && (
            <div className='text-muted-foreground/65 flex w-full min-w-0 justify-center empty:after:content-["—"]'>
              <RequestMetadataTags
                field='invoked_tools'
                isAdmin
                metadata={getLogMetadata(row.original)}
              />
            </div>
          ),
      })
    }
    columns.push(
      {
        accessorKey: 'is_stream',
        header: t('Transport'),
        meta: {
          displayFields: [
            ...(isAdmin ? [{ id: 'request_path', label: t('Endpoint') }] : []),
            { id: 'response_transport', label: t('Response mode') },
          ],
        },
        cell: ({ row, table }) => {
          const log = row.original
          const other = getLogMetadata(log)
          const visibility = table.getState().columnVisibility
          return (
            <div className='flex w-full min-w-0 flex-col items-start justify-center gap-1'>
              {visibility.response_transport !== false &&
                isTimingLogType(log.type) && (
                  <StreamModeCell
                    logType={log.type}
                    isStream={log.is_stream}
                    metadata={other}
                    showEndpoint={isAdmin && visibility.request_path !== false}
                  />
                )}
              {isAdmin &&
                visibility.request_path !== false &&
                isDisplayableLogType(log.type) &&
                log.type !== LOG_TYPE_ENUM.REFUND &&
                other?.request_path && (
                  <LogFilterValue
                    field='requestPath'
                    value={other.request_path.split('?', 1)[0]}
                  >
                    <TruncatedCell cellClassName='text-muted-foreground max-w-full font-mono text-xs'>
                      {other.request_path}
                    </TruncatedCell>
                  </LogFilterValue>
                )}
            </div>
          )
        },
      },
      {
        accessorKey: 'prompt_tokens',
        header: t('Token / Performance'),
        size: 355,
        cell: ({ row }) => (
          <TokenPerformanceCell
            log={row.original}
            other={getLogMetadata(row.original)}
            isAdmin={isAdmin}
          />
        ),
      },
      {
        accessorKey: 'quota',
        header: t('Cost'),
        cell: ({ row }) => {
          const log = row.original
          if (!isDisplayableLogType(log.type)) return null

          const quota = row.getValue('quota') as number
          const other = getLogMetadata(log)
          return (
            <LogCostDisplay
              log={log}
              quota={quota}
              other={other}
              showBillingSource={showBillingSource}
              isAdmin={
                isAdmin && source !== 'test' && canShowLogCost(log.type, other)
              }
            />
          )
        },
      }
    )

    function LogEventPreview({ log }: { log: UsageLog }) {
      const { t, i18n } = useTranslation()
      const other = getLogMetadata(log)

      const pricingData = usePricingData(
        !inlineEvents &&
          log.type === 2 &&
          other?.is_task === true &&
          other.billing_mode === 'tiered_expr'
      )
      const usageSchema = pluginUsageSchema(
        pricingData.models.find((model) => model.model_name === log.model_name),
        other?.admin_info?.task_plugin?.key
      )
      let segments: DetailSegment[] = []
      if (
        !inlineEvents ||
        !isConsumeLogType(log.type) ||
        isViolationFeeLog(other)
      ) {
        segments = buildDetailSegments(
          log,
          other,
          t,
          isAdmin,
          i18n.language,
          usageSchema
        )
      } else if (isAdmin && other?.admin_info?.quota_saturation) {
        segments = [{ text: t('Quota clamped'), danger: true }]
      } else if (other?.is_system_prompt_overwritten) {
        segments = [{ text: t('System Prompt Override'), danger: true }]
      } else {
        const event =
          getRetryDecisionReason(other?.retry_decision, t) ||
          renderLogContent(other, t) ||
          log.content
        if (event) segments = [{ text: event, muted: true }]
      }
      const primary = segments[0]
      const hasMore = segments.length > 1
      let periodLabel = ''
      if (primary?.period === 'peak') {
        periodLabel = t('Peak hours')
      } else if (primary?.period === 'off_peak') {
        periodLabel = t('Off-peak hours')
      } else if (primary?.period === 'standard') {
        periodLabel = t('Standard')
      }
      let primaryTextClass = 'text-foreground'
      if (primary?.muted) {
        primaryTextClass = 'text-muted-foreground/60'
      } else if (primary?.danger) {
        primaryTextClass = 'text-red-600 dark:text-red-400'
      }
      let detailPreview = <span className='text-muted-foreground/40'>—</span>
      const failedRequest =
        log.type === LOG_TYPE_ENUM.ERROR ||
        isFailedStreamResponse(other?.stream_status)
      if (failedRequest) {
        const failureReason =
          other?.stream_status?.end_error ||
          renderLogContent(other, t) ||
          log.content ||
          other?.stream_status?.errors?.at(-1)
        const failure = summarizeRequestFailure(failureReason, other)
        detailPreview = (
          <span className='text-destructive flex min-w-0 items-center gap-1.5 leading-snug'>
            {failure.statusCode ? (
              <span className='shrink-0 font-mono tabular-nums'>
                {failure.statusCode}
              </span>
            ) : null}{' '}
            <span className='truncate'>{t(failure.label)}</span>
          </span>
        )
      } else if (primary) {
        detailPreview = (
          <span
            className={cn(
              'truncate leading-snug group-hover:underline',
              primaryTextClass
            )}
          >
            {primary.period && (
              <BillingTierIcon tier={primary.period} label={periodLabel} />
            )}
            {primary.text}
            {hasMore && (
              <span className='text-muted-foreground/40 ml-0.5'>
                +{segments.length - 1}
              </span>
            )}
          </span>
        )
      } else if (log.content) {
        detailPreview = (
          <span className='text-muted-foreground truncate group-hover:underline'>
            {renderLogContent(other, t) ?? log.content}
          </span>
        )
      }

      if (inlineEvents && !failedRequest && !primary && !log.content) {
        return null
      }

      const detailsButton = (
        <Button
          variant='ghost'
          size='sm'
          data-log-event={inlineEvents || undefined}
          data-log-failure={failedRequest || undefined}
          aria-haspopup='dialog'
          type='button'
          className='group flex h-auto max-w-[200px] min-w-0 items-center justify-start gap-1 rounded-sm p-0 text-start text-xs font-normal hover:bg-transparent'
          onClick={() => onShowDetails({ log, initialView: 'technical' })}
          title={t('Click to view full details')}
        >
          {detailPreview}
        </Button>
      )

      return periodLabel ? (
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger render={detailsButton} />
            <TooltipContent>{periodLabel}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : (
        detailsButton
      )
    }

    if (!inlineEvents) {
      columns.push({
        accessorKey: 'content',
        header: t('Event'),
        cell: ({ row }) => <LogEventPreview log={row.original} />,
        size: 180,
        maxSize: 200,
      })
    }
    columns.push({
      id: 'request_details',
      header: t('Details'),
      enableSorting: false,
      enableHiding: false,
      meta: inlineEvents
        ? { displayFields: [{ id: 'content', label: t('Event') }] }
        : undefined,
      cell: ({ row, table }) => {
        const log = row.original
        const showEvent =
          inlineEvents &&
          table.getState().columnVisibility.content !== false &&
          hasLogEventDetail(log, isAdmin)
        return (
          <div className='flex items-center justify-end gap-1'>
            {showEvent && <LogEventPreview log={log} />}
            <Action
              size='icon-sm'
              label={`${t('Details')} · ${log.request_id || `#${log.id}`}`}
              tooltip={t('View details')}
              aria-haspopup='dialog'
              onClick={() => onShowDetails({ log, initialView: 'overview' })}
              className='size-8 rounded-sm border-0 p-0 hover:bg-transparent dark:hover:bg-transparent pointer-coarse:size-11'
            >
              <FileSearch aria-hidden='true' className='size-4' />
              {!inlineEvents && (
                <span
                  data-request-detail-caption
                  aria-hidden='true'
                  className='hidden text-[11px] leading-3 font-normal'
                >
                  {t('Details')}
                </span>
              )}
            </Action>
          </div>
        )
      },
    })

    if (headerFilters) {
      for (const column of columns) {
        const id =
          column.id ??
          ('accessorKey' in column ? String(column.accessorKey) : '')
        const filter = filterColumns[id]
        const title = column.header
        if (!filter || typeof title !== 'string') continue
        column.meta = {
          ...column.meta,
          headerAction: ({ table }: HeaderContext<UsageLog, unknown>) => (
            <LogHeaderFilter
              column={filter}
              title={title}
              isAdmin={isAdmin}
              logs={table.options.data}
              withReasoning={
                compact &&
                filter === 'model' &&
                table.getColumn('reasoning_effort')?.getIsVisible() === true
              }
            />
          ),
        }
      }
    }
    return columns
    // Log formatters read currency settings from the store.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    t,
    isAdmin,
    onShowDetails,
    showBillingSource,
    source,
    currency,
    compact,
    inlineEvents,
    headerFilters,
  ])
}
