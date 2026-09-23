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
import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  Clock3,
  Flame,
  ShieldCheck,
  TrendingDown,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StaggerContainer, StaggerItem } from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import { getUserQuotaDates } from '@/features/dashboard/api'
import { useSummaryCardsConfig } from '@/features/dashboard/hooks/use-dashboard-config'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { getSelfSubscriptionFull } from '@/features/subscriptions/api'
import { formatTimestamp } from '@/features/subscriptions/lib/format'
import { useStatus } from '@/hooks/use-status'
import { getCurrencyLabel, isCurrencyDisplayEnabled } from '@/lib/currency'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { computeTimeRange } from '@/lib/time'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { StatCard } from '../ui/stat-card'

const SUMMARY_SPARKLINE_BUCKETS = 12

type SummarySparklineKey = 'balance' | 'usage' | 'requests'

function getBucketIndex(
  timestamp: number,
  start: number,
  end: number,
  bucketCount: number
): number {
  if (end <= start) return 0
  const ratio = (timestamp - start) / (end - start)
  return Math.min(bucketCount - 1, Math.max(0, Math.floor(ratio * bucketCount)))
}

function buildSummarySparklines(
  data: QuotaDataItem[],
  currentBalance: number,
  start: number,
  end: number
): Record<SummarySparklineKey, number[]> {
  const usage = Array.from({ length: SUMMARY_SPARKLINE_BUCKETS }, () => 0)
  const requests = Array.from({ length: SUMMARY_SPARKLINE_BUCKETS }, () => 0)

  for (const item of data) {
    const timestamp = Number(item.created_at) || start
    const index = getBucketIndex(
      timestamp,
      start,
      end,
      SUMMARY_SPARKLINE_BUCKETS
    )
    usage[index] += Number(item.quota) || 0
    requests[index] += Number(item.count) || 0
  }

  let balance = currentBalance
  const balanceTrend = Array.from(
    { length: SUMMARY_SPARKLINE_BUCKETS },
    () => 0
  )

  for (let index = SUMMARY_SPARKLINE_BUCKETS - 1; index >= 0; index--) {
    balanceTrend[index] = Math.max(0, balance)
    balance += usage[index]
  }

  return {
    balance: balanceTrend,
    usage,
    requests,
  }
}

function getSummarySparkline(
  key: string,
  sparklineData: Record<SummarySparklineKey, number[]>
): number[] | undefined {
  if (key === 'usage') return sparklineData.usage
  if (key === 'requests') return sparklineData.requests
  return undefined
}

function getRunwayDays(
  remainQuota: number,
  recentUsage: number
): number | null {
  if (remainQuota <= 0 || recentUsage <= 0) return null
  const days = remainQuota / recentUsage
  if (!Number.isFinite(days)) return null
  return days
}

type HealthLevel = 'healthy' | 'caution' | 'critical'

function getHealthLevel(remainQuota: number, recentUsage: number): HealthLevel {
  if (remainQuota <= 0) return 'critical'
  const days = getRunwayDays(remainQuota, recentUsage)
  if (days !== null && days < 3) return 'caution'
  return 'healthy'
}

const HEALTH_CONFIG: Record<
  HealthLevel,
  { dotClass: string; labelKey: string }
> = {
  healthy: {
    dotClass: 'bg-success',
    labelKey: 'Healthy',
  },
  caution: {
    dotClass: 'bg-warning',
    labelKey: 'Low balance',
  },
  critical: {
    dotClass: 'bg-destructive',
    labelKey: 'Balance depleted',
  },
}

export function SummaryCards() {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  const { status, loading } = useStatus()

  const summaryTimeRange = useMemo(() => computeTimeRange(1), [])
  const remainQuota = Number(user?.quota ?? 0)
  const usedQuota = Number(user?.used_quota ?? 0)
  const requestCount = Number(user?.request_count ?? 0)

  const subscriptionsQuery = useQuery({
    queryKey: ['dashboard', 'self-subscriptions', user?.id],
    queryFn: async ({ signal }) => {
      const response = await getSelfSubscriptionFull(signal)
      if (!response.success || !response.data) {
        throw new Error('Subscription query failed')
      }
      return response.data
    },
    enabled: user != null,
    staleTime: 30 * 1000,
    refetchInterval: 60 * 1000,
    gcTime: 0,
    placeholderData: undefined,
  })
  const subscriptionSummary = useMemo(() => {
    const now = Date.now() / 1000
    const active = (subscriptionsQuery.data?.subscriptions ?? [])
      .map((record) => record.subscription)
      .filter(
        (subscription) =>
          subscription.status === 'active' && subscription.end_time > now
      )
    let remaining = 0
    let used = 0
    let unlimited = false
    let nextResetTime = 0
    for (const subscription of active) {
      const total = Number(subscription.amount_total) || 0
      const amountUsed = Math.max(0, Number(subscription.amount_used) || 0)
      used += amountUsed
      if (total <= 0) unlimited = true
      else remaining += Math.max(0, total - amountUsed)
      const reset = subscription.next_reset_time ?? 0
      if (reset > 0 && (nextResetTime === 0 || reset < nextResetTime)) {
        nextResetTime = reset
      }
    }
    return { count: active.length, remaining, used, unlimited, nextResetTime }
  }, [subscriptionsQuery.data])
  const hasSubscriptions = subscriptionSummary.count > 0

  const usageTrendQuery = useQuery({
    queryKey: [
      'dashboard',
      'overview',
      'summary-sparklines',
      user?.id,
      summaryTimeRange.start_timestamp,
      summaryTimeRange.end_timestamp,
    ],
    queryFn: async () =>
      requireServerSuccess(
        await getUserQuotaDates({
          start_timestamp: summaryTimeRange.start_timestamp,
          end_timestamp: summaryTimeRange.end_timestamp,
          default_time: 'hour',
        })
      ),
    staleTime: 60 * 1000,
    enabled: user != null,
  })

  const summaryValues = useMemo(() => {
    return {
      usedDisplay: formatQuota(usedQuota),
      requestCountDisplay: formatNumber(requestCount),
    }
  }, [requestCount, usedQuota])

  const currencyEnabledFromStore = isCurrencyDisplayEnabled()
  const statusCurrencyFlag =
    typeof status?.display_in_currency === 'boolean'
      ? Boolean(status.display_in_currency)
      : undefined
  const currencyEnabled =
    statusCurrencyFlag !== undefined
      ? statusCurrencyFlag
      : currencyEnabledFromStore
  const currencyLabel = currencyEnabled ? getCurrencyLabel() : 'Tokens'

  const sparklineData = useMemo(
    () =>
      buildSummarySparklines(
        usageTrendQuery.data?.data ?? [],
        remainQuota,
        summaryTimeRange.start_timestamp,
        summaryTimeRange.end_timestamp
      ),
    [
      remainQuota,
      summaryTimeRange.end_timestamp,
      summaryTimeRange.start_timestamp,
      usageTrendQuery.data?.data,
    ]
  )

  const recentUsage = useMemo(
    () =>
      (usageTrendQuery.data?.data ?? []).reduce(
        (total, item) => total + (Number(item.quota) || 0),
        0
      ),
    [usageTrendQuery.data?.data]
  )

  const healthLevel = getHealthLevel(remainQuota, recentUsage)
  const healthCfg = HEALTH_CONFIG[healthLevel]
  const runwayDays = getRunwayDays(remainQuota, recentUsage)

  const todayUsageDisplay = formatQuota(recentUsage)
  let runwayDisplay: string
  if (runwayDays !== null) {
    if (runwayDays < 1) {
      runwayDisplay = t('Less than 1 day left')
    } else if (runwayDays > 999) {
      runwayDisplay = `999+ ${t('days')}`
    } else {
      runwayDisplay = `~${formatNumber(Math.floor(runwayDays))} ${t('days')}`
    }
  } else if (remainQuota <= 0) {
    runwayDisplay = t('Balance depleted')
  } else {
    runwayDisplay = t('No recent usage')
  }

  let creditTitle = t('Credit remaining')
  let creditValue = formatQuota(remainQuota)
  let creditStatus = t(healthCfg.labelKey)
  let creditDotClass = healthCfg.dotClass
  let usageLabel = t('Last 24h usage')
  let usageValue = todayUsageDisplay
  let timingLabel = t('Runway')
  let timingValue = runwayDisplay
  let TimingIcon = ShieldCheck
  if (hasSubscriptions) {
    creditTitle = t('Subscription remaining')
    creditValue = subscriptionSummary.unlimited
      ? t('Unlimited')
      : formatQuota(subscriptionSummary.remaining)
    creditStatus = t('Active')
    creditDotClass = 'bg-success'
    if (!subscriptionSummary.unlimited && subscriptionSummary.remaining <= 0) {
      creditStatus = t('Subscription quota depleted')
      creditDotClass = 'bg-destructive'
    }
    usageLabel = t('Current period usage')
    usageValue = formatQuota(subscriptionSummary.used)
    timingLabel = t('Next subscription refresh')
    timingValue =
      subscriptionSummary.nextResetTime > 0
        ? formatTimestamp(subscriptionSummary.nextResetTime)
        : t('No Reset')
    TimingIcon = Clock3
  } else if (runwayDays !== null && runwayDays < 3) {
    TimingIcon = TrendingDown
  }

  const items = useSummaryCardsConfig({
    ...summaryValues,
    todayUsageDisplay,
    currencyEnabled,
    currencyLabel,
  }).map((config, index) => {
    const tones = ['accent-1', 'accent-2', 'accent-3'] as const

    return {
      key: config.key,
      title: config.title,
      value: config.value,
      desc: config.description,
      icon: config.icon,
      tone: tones[index] ?? 'accent-3',
      sparkline:
        config.key === 'todayUsage'
          ? sparklineData.usage
          : getSummarySparkline(config.key, sparklineData),
      sparklineVariant: 'line' as const,
    }
  })

  return (
    <div className='bg-card overflow-hidden rounded-2xl border shadow-xs'>
      <div className='grid xl:grid-cols-[minmax(0,1fr)_19rem]'>
        <div className='flex flex-col gap-2.5 p-3 sm:gap-3 sm:p-5'>
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div className='flex flex-col gap-1'>
              <h3 className='text-sm font-semibold sm:text-base'>
                {t('Usage at a glance')}
              </h3>
              <p className='text-muted-foreground text-xs sm:text-sm'>
                {t('Monitor balance, usage, and request volume')}
              </p>
            </div>
          </div>
          <StaggerContainer className='grid grid-cols-3 gap-1.5 sm:gap-3'>
            {items.map((it) => (
              <StaggerItem
                key={it.key}
                className='bg-background/60 rounded-lg border px-2 py-1.5 sm:rounded-xl sm:p-3'
              >
                <StatCard
                  title={it.title}
                  value={it.value}
                  description={it.desc}
                  icon={it.icon}
                  tone={it.tone}
                  sparkline={it.sparkline}
                  sparklineVariant={it.sparklineVariant}
                  loading={loading}
                  compactMobile
                />
              </StaggerItem>
            ))}
          </StaggerContainer>
        </div>

        <div
          role='group'
          aria-label={t('Credit remaining')}
          className='flex flex-col justify-between gap-3 border-t bg-[linear-gradient(135deg,color-mix(in_oklch,var(--overview-accent-2)_12%,var(--background))_0%,color-mix(in_oklch,oklch(0.82_0.04_155)_8%,var(--background))_48%,color-mix(in_oklch,var(--overview-accent-1)_7%,var(--background))_100%)] p-3 sm:gap-4 sm:p-5 xl:border-t-0 xl:border-l'
        >
          {subscriptionsQuery.isLoading && (
            <LoadingState size='sm' className='min-h-32' />
          )}
          {subscriptionsQuery.isError && (
            <ErrorState
              className='min-h-32'
              onRetry={() => void subscriptionsQuery.refetch()}
            />
          )}
          {!subscriptionsQuery.isLoading && !subscriptionsQuery.isError && (
            <div className='flex flex-col gap-2 sm:gap-3'>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground text-xs font-medium'>
                  {creditTitle}
                </span>
                <span className='flex items-center gap-1.5'>
                  <span
                    className={cn('size-1.5 rounded-full', creditDotClass)}
                    aria-hidden='true'
                  />
                  <span className='text-muted-foreground text-[11px] font-medium'>
                    {creditStatus}
                  </span>
                </span>
              </div>

              <div className='font-mono text-xl font-semibold tracking-tight sm:text-2xl'>
                {creditValue}
              </div>

              <div className='grid grid-cols-2 gap-2'>
                <div className='bg-background/60 rounded-lg px-2.5 py-2'>
                  <div className='text-muted-foreground flex items-center gap-1 text-[11px] leading-none font-medium'>
                    <Flame className='size-3 shrink-0' aria-hidden='true' />
                    <span className='truncate'>{usageLabel}</span>
                  </div>
                  <div className='text-foreground mt-1.5 truncate text-xs font-semibold tabular-nums'>
                    {usageValue}
                  </div>
                </div>
                <div className='bg-background/60 rounded-lg px-2.5 py-2'>
                  <div className='text-muted-foreground flex items-center gap-1 text-[11px] leading-none font-medium'>
                    <TimingIcon
                      className='size-3 shrink-0'
                      aria-hidden='true'
                    />
                    <span className='truncate'>{timingLabel}</span>
                  </div>
                  <div
                    className={cn(
                      'mt-1.5 text-xs font-semibold tabular-nums',
                      !hasSubscriptions &&
                        healthLevel === 'critical' &&
                        'text-destructive',
                      !hasSubscriptions &&
                        healthLevel === 'caution' &&
                        'text-warning'
                    )}
                  >
                    {timingValue}
                  </div>
                </div>
              </div>
            </div>
          )}

          <Button className='justify-between' render={<Link to='/wallet' />}>
            <span>
              {hasSubscriptions ? t('My Subscriptions') : t('Wallet')}
            </span>
            <ArrowRight data-icon='inline-end' />
          </Button>
        </div>
      </div>
    </div>
  )
}
