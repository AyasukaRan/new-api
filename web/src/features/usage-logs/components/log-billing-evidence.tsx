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
import type { TFunction } from 'i18next'
import { AlertTriangle, Cloud, Headphones, Monitor } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Label } from '@/components/ui/label'
import { DynamicPricingBreakdown } from '@/features/pricing/components/dynamic-pricing-breakdown'
import { usePricingData } from '@/features/pricing/hooks/use-pricing-data'
import { BILLING_PRICING_VARS } from '@/features/pricing/lib/billing-expr'
import { compileBillingExpression } from '@/features/pricing/lib/billing-expression/parser'
import { getCachePriceLabels, getCachePricingMode } from '@/features/pricing/lib/cache-pricing'
import { pluginUsageSchema } from '@/features/pricing/lib/plugin-pricing'
import { toIntlLocale } from '@/i18n/languages'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatLogQuota, formatNumber, formatTokens } from '@/lib/format'

import type { UsageLog } from '../data/schema'
import {
  hasRecordedAudioUsage,
  hasRecordedBillingRates,
} from '../lib/charge-breakdown'
import {
  decodeBillingExprB64,
  getTieredBillingSummary,
  getLogCostEstimate,
  getLogFreeQuota,
  hasAnyCacheTokens,
  isViolationFeeLog,
} from '../lib/format'
import {
  isPerCallBilling,
  isDisplayableLogType,
  isConsumeLogType,
} from '../lib/utils'
import { USAGE_BILLING_PATH, type LogOtherData } from '../types'
import { DetailRow, DetailSection } from './dialogs/log-detail-layout'

function formatRatio(ratio: number | undefined): string {
  if (ratio == null) return '-'
  return ratio.toFixed(4)
}

function getUsageBillingPathLabel(
  t: TFunction,
  adminInfo: LogOtherData['admin_info']
): string {
  switch (adminInfo?.usage_billing_path) {
    case USAGE_BILLING_PATH.LOCAL:
      return t('Local Billing')
    case USAGE_BILLING_PATH.OPENAI:
      return t('Upstream Response (billing-usage-openai)')
    case USAGE_BILLING_PATH.OPENAI_ESTIMATED:
      return t('Upstream Response (billing-usage-openai-estimated)')
    case USAGE_BILLING_PATH.ANTHROPIC:
      return t('Upstream Response (billing-usage-anthropic)')
    case USAGE_BILLING_PATH.ANTHROPIC_ESTIMATED:
      return t('Upstream Response (billing-usage-anthropic-estimated)')
    case USAGE_BILLING_PATH.GEMINI:
      return t('Upstream Response (billing-usage-gemini)')
    case USAGE_BILLING_PATH.GEMINI_ESTIMATED:
      return t('Upstream Response (billing-usage-gemini-estimated)')
    case USAGE_BILLING_PATH.UPSTREAM:
      return t('Upstream Response')
    default:
      return adminInfo?.local_count_tokens
        ? t('Local Billing')
        : t('Upstream Response')
  }
}

function isUsageBillingPathLocal(
  adminInfo: LogOtherData['admin_info']
): boolean {
  if (adminInfo?.usage_billing_path) {
    return adminInfo.usage_billing_path === USAGE_BILLING_PATH.LOCAL
  }
  return adminInfo?.local_count_tokens === true
}

function quotaSaturationKindLabel(
  kind: 'overflow' | 'underflow' | 'nan',
  t: (key: string) => string
): string {
  if (kind === 'overflow') return t('Overflow')
  if (kind === 'underflow') return t('Underflow')
  return t('Invalid (NaN)')
}

function BillingBreakdown(props: {
  log: UsageLog
  other: LogOtherData
  isAdmin: boolean
  showCostEstimate: boolean
  legacy: boolean
}) {
  const { t } = useTranslation()
  const { log, other, isAdmin } = props
  const cost =
    isAdmin && props.showCostEstimate ? getLogCostEstimate(other) : null
  const isPerCall = isPerCallBilling(other.model_price)
  const isClaude = other.claude === true
  // OpenAI 12-hour cache writes have their own price, listed like Claude's.
  const hasCacheWrite12h = (other.cache_creation_tokens_12h || 0) > 0
  const isTieredExpr = other.billing_mode === 'tiered_expr'
  const tieredSummary = getTieredBillingSummary(other, log.model_name)

  const rows: Array<{ label: string; value: string }> = []
  const priceOpts = {
    digitsLarge: 4,
    digitsSmall: other.metered_usage ? 8 : 6,
    abbreviate: false,
  }
  const fmtPrice = (usd: number) => formatBillingCurrencyFromUSD(usd, priceOpts)
  const baseInputUSD = other.model_ratio != null ? other.model_ratio * 2.0 : 0

  if (isTieredExpr) {
    rows.push({
      label: t('Billing Mode'),
      value: t('Dynamic Pricing'),
    })
    if (tieredSummary) {
      if (tieredSummary.tier.label) {
        rows.push({
          label: t('Matched Tier'),
          value: tieredSummary.tier.label,
        })
      }
      for (const entry of tieredSummary.priceEntries) {
        rows.push({
          label: t(entry.shortLabel),
          value: `${fmtPrice(entry.price)}/${entry.unit ? t(entry.unit) : 'M'}`,
        })
      }
    } else {
      rows.push({
        label: t('Matched Tier'),
        value: other.matched_tier || t('No matching results'),
      })
    }
  } else if (isPerCall) {
    rows.push({ label: t('Billing Mode'), value: t('Per-call') })
    if (other.model_price != null) {
      rows.push({
        label: t('Model Price'),
        value: fmtPrice(other.model_price),
      })
    }
  } else {
    if (
      props.legacy ||
      other.model_ratio != null ||
      other.model_price != null
    ) {
      rows.push({ label: t('Billing Mode'), value: t('Per-token') })
    }
    if (other.model_ratio != null) {
      rows.push({
        label: t('Input'),
        value: `${fmtPrice(baseInputUSD)}/M`,
      })
    }
    if (other.completion_ratio != null && other.model_ratio != null) {
      rows.push({
        label: t('Output'),
        value: `${fmtPrice(baseInputUSD * other.completion_ratio)}/M`,
      })
    }
  }

  const userGR = other.user_group_ratio
  const isUserGR = userGR != null && Number.isFinite(userGR) && userGR !== -1
  const effectiveGR = isUserGR ? userGR : other.group_ratio
  if (effectiveGR != null && Number.isFinite(effectiveGR)) {
    rows.push({
      label: isUserGR ? t('User Exclusive Ratio') : t('Group Ratio'),
      value: `${formatRatio(effectiveGR)}x`,
    })
  }

  // The serving channel's markup is already folded into the ratio above; this
  // row is the decomposition, and the backend only emits it for admins.
  const channelRatio = other.admin_info?.channel_ratio
  if (isAdmin && channelRatio != null && Number.isFinite(channelRatio)) {
    rows.push({
      label: t('Channel Ratio (included above)'),
      value: `${formatRatio(channelRatio)}x`,
    })
  }

  if (
    !isTieredExpr &&
    (isClaude || hasCacheWrite12h) &&
    hasAnyCacheTokens(other)
  ) {
    if (other.cache_ratio != null && other.cache_ratio !== 1) {
      rows.push({
        label: t('Cache Read'),
        value: `${fmtPrice(baseInputUSD * other.cache_ratio)}/M`,
      })
    }
    if (
      other.cache_creation_ratio != null &&
      other.cache_creation_ratio !== 1
    ) {
      rows.push({
        label: hasCacheWrite12h
          ? t('Cache Write (30m)')
          : t('Cache Write'),
        value: `${fmtPrice(baseInputUSD * other.cache_creation_ratio)}/M`,
      })
    }
    if (
      other.cache_creation_ratio_5m != null &&
      other.cache_creation_ratio_5m !== 0
    ) {
      rows.push({
        label: t('Cache Write (5m)'),
        value: `${fmtPrice(baseInputUSD * other.cache_creation_ratio_5m)}/M`,
      })
    }
    if (
      other.cache_creation_ratio_1h != null &&
      other.cache_creation_ratio_1h !== 0
    ) {
      rows.push({
        label: t('Cache Write (1h)'),
        value: `${fmtPrice(baseInputUSD * other.cache_creation_ratio_1h)}/M`,
      })
    }
    if (
      other.cache_creation_ratio_12h != null &&
      other.cache_creation_ratio_12h !== 0
    ) {
      rows.push({
        label: t('Cache Write (12h)'),
        value: `${fmtPrice(baseInputUSD * other.cache_creation_ratio_12h)}/M`,
      })
    }
  }

  if (!isTieredExpr) {
    if (other.audio_ratio != null && other.audio_ratio !== 1) {
      rows.push({
        label: t('Audio input'),
        value: `${fmtPrice(baseInputUSD * other.audio_ratio)}/M`,
      })
    }

    if (
      other.audio_completion_ratio != null &&
      other.audio_completion_ratio !== 1
    ) {
      rows.push({
        label: t('Audio output'),
        value: `${fmtPrice(baseInputUSD * other.audio_completion_ratio)}/M`,
      })
    }

    if (other.image_ratio != null && other.image_ratio !== 1) {
      rows.push({
        label: t('Image input'),
        value: `${fmtPrice(baseInputUSD * other.image_ratio)}/M`,
      })
    }
  }

  if (other.web_search && other.web_search_call_count) {
    rows.push({
      label: t('Web Search'),
      value: `${other.web_search_call_count}x${other.web_search_price ? ` (${fmtPrice(other.web_search_price)})` : ''}`,
    })
  }

  if (other.file_search && other.file_search_call_count) {
    rows.push({
      label: t('File Search'),
      value: `${other.file_search_call_count}x${other.file_search_price ? ` (${fmtPrice(other.file_search_price)})` : ''}`,
    })
  }

  if (other.image_generation_call && other.image_generation_call_price) {
    rows.push({
      label: t('Image Generation'),
      value: fmtPrice(other.image_generation_call_price),
    })
  }

  if (other.audio_input_seperate_price && other.audio_input_price) {
    rows.push({
      label: t('Audio Input Price'),
      value: fmtPrice(other.audio_input_price),
    })
  }

  if (
    isAdmin &&
    other.admin_info &&
    (props.legacy ||
      other.admin_info.usage_billing_path ||
      other.admin_info.local_count_tokens != null)
  ) {
    rows.push({
      label: t('Billing Path'),
      value: getUsageBillingPathLabel(t, other.admin_info),
    })
  }

  const usageFacts =
    other.usage_facts != null &&
    typeof other.usage_facts === 'object' &&
    !Array.isArray(other.usage_facts)
      ? Object.entries(other.usage_facts)
      : []

  return (
    <DetailSection
      label={props.legacy ? t('Billing Details') : t('Pricing rules')}
    >
      {rows.map((row) => (
        <DetailRow key={row.label} label={row.label} value={row.value} mono />
      ))}
      {usageFacts.length > 0 && (
        <>
          <Label className='text-xs font-semibold'>
            {t('Usage parameters')}
          </Label>
          {usageFacts.map(([key, value]) => (
            <DetailRow
              key={`usage-fact-${key}`}
              label={key}
              value={String(value)}
              mono
            />
          ))}
        </>
      )}
      {props.legacy && (
        <DetailRow
          label={t('Total Cost')}
          value={formatLogQuota(log.quota)}
          mono
        />
      )}
      {props.legacy && getLogFreeQuota(other, log.quota) > 0 && (
        <>
          <DetailRow
            label={t('Free allowance deduction')}
            value={formatLogQuota(getLogFreeQuota(other, log.quota))}
            mono
          />
          <DetailRow
            label={t('Wallet charge')}
            value={formatLogQuota(
              log.quota - getLogFreeQuota(other, log.quota)
            )}
            mono
          />
        </>
      )}
      {isAdmin && props.showCostEstimate && (
        <>
          {cost && (
            <>
              <DetailRow
                label={t('Base model amount')}
                value={formatLogQuota(cost.base_quota)}
                mono
              />
              <DetailRow
                label={t('Upstream cost multiplier')}
                value={`${formatRatio(cost.cost_ratio)}x`}
                mono
              />
            </>
          )}
          {props.legacy && (
            <>
              <DetailRow
                label={t('Upstream cost')}
                value={
                  cost ? formatLogQuota(cost.cost_quota) : t('Not recorded')
                }
                mono
              />
              <DetailRow
                label={t('Gross profit')}
                value={
                  cost ? formatLogQuota(cost.profit_quota) : t('Not recorded')
                }
                mono
              />
            </>
          )}
          {(cost || props.legacy) && (
            <p className='text-muted-foreground text-xs'>
              {t(
                'Gross profit excludes free allowance deductions and subtracts upstream cost. Subscription usage is not cash revenue.'
              )}
            </p>
          )}
        </>
      )}
    </DetailSection>
  )
}

function TokenBreakdown(props: {
  log: UsageLog
  other: LogOtherData
  legacy: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { log, other } = props

  const compiled = compileBillingExpression(decodeBillingExprB64(other.expr_b64))
  const cacheMode = getCachePricingMode(
    compiled.status === 'ready' ? compiled.variables : [],
    log.model_name
  )
  const promptTokens = log.prompt_tokens || 0
  const completionTokens = log.completion_tokens || 0
  const cacheRead = other.cache_tokens || 0
  const cacheWrite = other.cache_creation_tokens || 0
  const cacheWrite5m = other.cache_creation_tokens_5m || 0
  const cacheWrite1h = other.cache_creation_tokens_1h || 0
  const cacheWrite12h = other.cache_creation_tokens_12h || 0
  // cache_creation_tokens counts OpenAI 30-minute and 12-hour writes together.
  const cacheWrite30m =
    cacheWrite12h > 0 ? Math.max(cacheWrite - cacheWrite12h, 0) : 0
  const hasTokens = promptTokens > 0 || completionTokens > 0

  if (props.legacy && !hasTokens && !other.billing_tokens) return null

  const rows: Array<{ label: string; value: string }> = []

  if (props.legacy) {
    rows.push({
      label: t('Input Tokens'),
      value: formatNumber(promptTokens, locale),
    })
    rows.push({
      label: t('Output Tokens'),
      value: formatNumber(completionTokens, locale),
    })
  }

  if (cacheRead > 0) {
    rows.push({
      label: t('Cache Read'),
      value: formatNumber(cacheRead, locale),
    })
  }

  if (other.image_cache_tokens !== undefined) {
    rows.push({
      label: t('Image Cache'),
      value: formatNumber(other.image_cache_tokens, locale),
    })
  }

  if (
    cacheWrite > 0 &&
    cacheWrite5m === 0 &&
    cacheWrite1h === 0 &&
    cacheWrite12h === 0
  ) {
    rows.push({
      label: t('Cache Write'),
      value: formatNumber(cacheWrite, locale),
    })
  }

  if (cacheWrite5m > 0) {
    rows.push({
      label: t('Cache Write (5m)'),
      value: formatNumber(cacheWrite5m, locale),
    })
  }

  if (cacheWrite1h > 0) {
    rows.push({
      label: t('Cache Write (1h)'),
      value: formatNumber(cacheWrite1h, locale),
    })
  }

  if (cacheWrite30m > 0) {
    rows.push({
      label: t('Cache Write (30m)'),
      value: formatNumber(cacheWrite30m, locale),
    })
  }

  if (cacheWrite12h > 0) {
    rows.push({
      label: t('Cache Write (12h)'),
      value: formatNumber(cacheWrite12h, locale),
    })
  }

  if (other.image && other.image_output) {
    rows.push({
      label: t('Image Tokens'),
      value: formatNumber(other.image_output, locale),
    })
  }

  const hasBillableTokens =
    other.billing_tokens &&
    (props.legacy ||
      BILLING_PRICING_VARS.some(({ key }) =>
        Number.isFinite(other.billing_tokens?.[key])
      ))
  if (!rows.length && !hasBillableTokens) return null

  return (
    <DetailSection label={t('Token Breakdown')}>
      {rows.map((row) => (
        <DetailRow key={row.label} label={row.label} value={row.value} mono />
      ))}
      {hasBillableTokens && (
        <div
          role='group'
          aria-label={t('Billable token breakdown')}
          className='space-y-2'
        >
          <Label className='text-xs font-semibold'>
            {t('Billable token breakdown')}
          </Label>
          {BILLING_PRICING_VARS.map((variable) => {
            const count = other.billing_tokens?.[variable.key]
            if (count === undefined || !Number.isFinite(count)) return null
            return (
              <DetailRow
                key={variable.key}
                label={t(getCachePriceLabels(variable, cacheMode).shortLabel)}
                value={formatNumber(count, locale)}
                mono
              />
            )
          })}
        </div>
      )}
    </DetailSection>
  )
}

function RecordedPricingRules(props: {
  log: UsageLog
  other: LogOtherData
  pluginKey?: string
}) {
  const { other } = props
  const pricingData = usePricingData(true)
  const billingUsageSchema = pluginUsageSchema(
    pricingData.models.find(
      (model) => model.model_name === props.log.model_name
    ),
    props.pluginKey
  )
  return (
    <DynamicPricingBreakdown
      modelName={props.log.model_name}
      compact
      billingExpr={decodeBillingExprB64(other.expr_b64 ?? '')}
      matchedTierLabel={other.matched_tier}
      matchedBillingUnit={other.billing_unit}
      matchedFixedPrice={other.fixed_price}
      requestRules={other.request_rules}
      hideCacheColumns={!hasAnyCacheTokens(other)}
      usageSchema={billingUsageSchema}
      usageFacts={other.usage_facts}
    />
  )
}

export function LogBillingEvidence(props: {
  log: UsageLog
  other: LogOtherData
  isAdmin: boolean
  legacy?: boolean
  testLog?: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { other } = props
  const isConsume = isConsumeLogType(props.log.type)
  const isViolation = isViolationFeeLog(other)
  const isSubscription =
    other.billing_source === 'subscription' ||
    other.billing_source === 'free_allowance'
  const isTieredBilling =
    !isViolation &&
    (isConsume || !props.legacy) &&
    other.billing_mode === 'tiered_expr'
  const hasAudioTokens = hasRecordedAudioUsage(other)
  const showRates = props.legacy
    ? isConsume
    : hasRecordedBillingRates(other, props.isAdmin, props.log.type)
  return (
    <div className='space-y-3'>
      {props.isAdmin && other?.admin_info?.quota_saturation && (
        <DetailSection
          icon={<AlertTriangle className='size-3.5' aria-hidden='true' />}
          label={t('Quota clamped')}
          variant='danger'
        >
          <p className='mb-1 text-xs wrap-break-word'>
            {t('Quota saturation protection triggered')}
          </p>
          <DetailRow
            label={t('Kind')}
            value={quotaSaturationKindLabel(
              other.admin_info.quota_saturation.kind,
              t
            )}
          />
          <DetailRow
            label={t('Original value')}
            value={String(other.admin_info.quota_saturation.original)}
            mono
          />
          <DetailRow
            label={t('Clamped to')}
            value={String(other.admin_info.quota_saturation.clamped)}
            mono
          />
          <DetailRow
            label={t('Operation')}
            value={other.admin_info.quota_saturation.op}
            mono
          />
        </DetailSection>
      )}

      {/* Violation fee info */}
      {isViolation && other && (
        <DetailSection
          icon={<AlertTriangle className='size-3.5' aria-hidden='true' />}
          label={t('Violation Fee')}
          variant='danger'
        >
          {other.violation_fee_code && (
            <DetailRow
              label={t('Violation Code')}
              value={other.violation_fee_code}
              mono
            />
          )}
          {other.violation_fee_marker && (
            <DetailRow
              label={t('Violation Marker')}
              value={other.violation_fee_marker}
            />
          )}
          <DetailRow
            label={t('Fee Amount')}
            value={formatLogQuota(other.fee_quota ?? props.log.quota)}
            mono
          />
        </DetailSection>
      )}

      {/* Audio/WebSocket token breakdown */}
      {hasAudioTokens && other && (
        <DetailSection
          icon={<Headphones className='size-3.5' aria-hidden='true' />}
          iconTone='chart-4'
          label={t('Audio Tokens')}
        >
          {other.audio_input != null && other.audio_input > 0 && (
            <DetailRow
              label={t('Audio Input')}
              value={formatTokens(other.audio_input)}
              mono
            />
          )}
          {other.audio_output != null && other.audio_output > 0 && (
            <DetailRow
              label={t('Audio Output')}
              value={formatTokens(other.audio_output)}
              mono
            />
          )}
          {other.text_input != null && other.text_input > 0 && (
            <DetailRow
              label={t('Text Input')}
              value={formatTokens(other.text_input)}
              mono
            />
          )}
          {other.text_output != null && other.text_output > 0 && (
            <DetailRow
              label={t('Text Output')}
              value={formatTokens(other.text_output)}
              mono
            />
          )}
        </DetailSection>
      )}

      {/* Token breakdown (for consume/error types with token data) */}
      {isDisplayableLogType(props.log.type) && other && (
        <TokenBreakdown log={props.log} other={other} legacy={!!props.legacy} />
      )}

      {/* Billing breakdown (consume type) */}
      {showRates && !isViolation && (
        <BillingBreakdown
          log={props.log}
          other={other}
          isAdmin={props.isAdmin}
          showCostEstimate={!props.testLog && props.log.type === 2}
          legacy={!!props.legacy}
        />
      )}

      {other?.metered_usage && (
        <DetailSection
          label={t(
            other.metered_usage_estimated ? 'Estimated usage' : 'Billable usage'
          )}
        >
          <DetailRow
            label={t(other.metered_usage.unit)}
            value={formatNumber(other.metered_usage.count, locale)}
          />
          {other.metered_usage_basis === 'request_text' && (
            <DetailRow
              label={t('Usage basis')}
              value={t('Submitted text; not an upstream usage receipt')}
            />
          )}
          {other.metered_usage_basis === 'local_pagination' && (
            <DetailRow
              label={t('Usage basis')}
              value={t('Locally counted document pages')}
            />
          )}
          {other.metered_usage_engine && (
            <DetailRow
              label={t('Pagination engine')}
              value={other.metered_usage_engine}
            />
          )}
        </DetailSection>
      )}
      {props.isAdmin && other?.admin_info?.cost_unconfirmed && (
        <DetailRow
          label={t('Upstream cost')}
          value={t('Unconfirmed after interruption')}
        />
      )}

      {/* Tiered pricing breakdown (when billing_mode is tiered_expr) */}
      {isTieredBilling && other?.expr_b64 && (
        <DetailSection label={t('Dynamic Pricing')}>
          {other.image_count !== undefined && (
            <DetailRow
              label={t('Billable image count')}
              value={other.image_count}
            />
          )}
          <RecordedPricingRules
            log={props.log}
            other={other}
            pluginKey={
              props.isAdmin ? other.admin_info?.task_plugin?.key : undefined
            }
          />
        </DetailSection>
      )}

      {/* Admin billing mode indicator for non-consume */}
      {props.isAdmin &&
        !showRates &&
        props.log.type !== 6 &&
        other.admin_info &&
        (props.legacy ||
          other.admin_info.usage_billing_path ||
          other.admin_info.local_count_tokens != null) && (
          <DetailRow
            label={t('Billing Path')}
            value={
              <span className='flex items-center gap-1'>
                {isUsageBillingPathLocal(other.admin_info) ? (
                  <Monitor className='size-3 text-blue-500' />
                ) : (
                  <Cloud className='size-3 text-emerald-500' />
                )}
                <span className='text-xs'>
                  {getUsageBillingPathLabel(t, other.admin_info)}
                </span>
              </span>
            }
          />
        )}

      {/* Subscription billing details */}
      {isSubscription && other && (
        <DetailSection
          label={t(
            other.billing_source === 'free_allowance'
              ? 'Free allowance'
              : 'Subscription Billing'
          )}
        >
          {!props.legacy &&
            !other.subscription_plan_id &&
            !other.subscription_id &&
            other.subscription_pre_consumed == null &&
            !other.subscription_post_delta &&
            other.subscription_consumed == null &&
            other.subscription_remain == null && (
              <DetailRow
                label={t('Billing Source')}
                value={t(
                  other.billing_source === 'free_allowance'
                    ? 'Free allowance'
                    : 'Subscription Billing'
                )}
              />
            )}
          {other.subscription_plan_id && (
            <DetailRow
              label={t('Plan')}
              value={`#${other.subscription_plan_id} ${other.subscription_plan_title || ''}`.trim()}
            />
          )}
          {other.subscription_id && (
            <DetailRow
              label={t('Instance')}
              value={`#${other.subscription_id}`}
              mono
            />
          )}
          {other.subscription_pre_consumed != null && (
            <DetailRow
              label={t('Pre-consumed')}
              value={formatLogQuota(other.subscription_pre_consumed)}
              mono
            />
          )}
          {other.subscription_post_delta != null &&
            other.subscription_post_delta !== 0 && (
              <DetailRow
                label={t('Post Delta')}
                value={formatLogQuota(other.subscription_post_delta)}
                mono
              />
            )}
          {other.subscription_consumed != null && (
            <DetailRow
              label={t('Final Consumed')}
              value={formatLogQuota(other.subscription_consumed)}
              mono
            />
          )}
          {other.subscription_remain != null && (
            <DetailRow
              label={t('Remaining')}
              value={`${formatLogQuota(other.subscription_remain)}${other.subscription_total != null ? ` / ${formatLogQuota(other.subscription_total)}` : ''}`}
              mono
            />
          )}
        </DetailSection>
      )}
    </div>
  )
}
