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
  Layers,
  ShieldCheck,
  TrendingDown,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StaggerContainer, StaggerItem } from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import { useSummaryCardsConfig } from '@/features/dashboard/hooks/use-dashboard-config'
import { usePersonalUsage } from '@/features/dashboard/hooks/use-personal-usage'
import { getSelfSubscriptionFull } from '@/features/subscriptions/api'
import { formatTimestamp } from '@/features/subscriptions/lib/format'
import { useStatus } from '@/hooks/use-status'
import { toIntlLocale } from '@/i18n/languages'
import { getCurrencyLabel, isCurrencyDisplayEnabled } from '@/lib/currency'
import { formatCompactNumber, formatNumber, formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { StatCard } from '../ui/stat-card'

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
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const user = useAuthStore((state) => state.auth.user)
  const { status, loading } = useStatus()

  const usageTrendQuery = usePersonalUsage()
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
    return {
      count: active.length,
      remaining,
      used,
      unlimited,
      nextResetTime,
      freeOnly:
        active.length > 0 &&
        active.every((subscription) => subscription.free_allowance),
    }
  }, [subscriptionsQuery.data])
  const hasSubscriptions = subscriptionSummary.count > 0

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

  const usageStats = usageTrendQuery.data
  const sparklines = useMemo(() => {
    if (!usageStats?.rows.length) return undefined
    return {
      usage: usageStats.points.map((point) => point.usage),
      requests: usageStats.points.map((point) => point.requests),
      tokens: usageStats.points.map((point) => point.tokens),
    }
  }, [usageStats])
  const recentUsage = usageStats?.totals.usage ?? 0
  const healthLevel = getHealthLevel(remainQuota, recentUsage)
  const healthCfg = HEALTH_CONFIG[healthLevel]
  const runwayDays = getRunwayDays(remainQuota, recentUsage)

  const usagePlaceholder = usageTrendQuery.isPending
    ? t('Loading...')
    : t('Unavailable')
  const todayUsageDisplay = usageStats
    ? formatQuota(recentUsage)
    : usagePlaceholder
  let runwayDisplay: string
  if (runwayDays !== null) {
    if (runwayDays < 1) {
      runwayDisplay = t('Less than 1 day left')
    } else if (runwayDays > 999) {
      runwayDisplay = `999+ ${t('days')}`
    } else {
      runwayDisplay = `~${formatNumber(Math.floor(runwayDays), locale)} ${t('days')}`
    }
  } else if (remainQuota <= 0) {
    runwayDisplay = t('Balance depleted')
  } else {
    runwayDisplay = usageStats ? t('No recent usage') : usagePlaceholder
  }

  let creditTitle = t('Credit remaining')
  let creditValue = formatQuota(remainQuota)
  let creditStatus =
    remainQuota <= 0 || usageStats ? t(healthCfg.labelKey) : undefined
  let creditDotClass = healthCfg.dotClass
  let timingLabel = t('Runway')
  let timingValue = runwayDisplay
  let TimingIcon = ShieldCheck
  if (hasSubscriptions) {
    creditTitle = t(
      subscriptionSummary.freeOnly
        ? 'Free allowance remaining'
        : 'Subscription remaining'
    )
    creditValue = subscriptionSummary.unlimited
      ? t('Unlimited')
      : formatQuota(subscriptionSummary.remaining)
    creditStatus = t('Active')
    creditDotClass = 'bg-success'
    if (!subscriptionSummary.unlimited && subscriptionSummary.remaining <= 0) {
      creditStatus = t(
        subscriptionSummary.freeOnly
          ? 'Free allowance depleted'
          : 'Subscription quota depleted'
      )
      creditDotClass = 'bg-destructive'
    }
    timingLabel = t(
      subscriptionSummary.freeOnly
        ? 'Next allowance refresh'
        : 'Next subscription refresh'
    )
    timingValue =
      subscriptionSummary.nextResetTime > 0
        ? formatTimestamp(subscriptionSummary.nextResetTime)
        : t('No Reset')
    TimingIcon = Clock3
  } else if (runwayDays !== null && runwayDays < 3) {
    TimingIcon = TrendingDown
  }

  const items = useSummaryCardsConfig({
    usedDisplay: formatQuota(usedQuota),
    requestCountDisplay: formatNumber(requestCount, locale),
    todayUsageDisplay,
    currencyEnabled,
    currencyLabel,
  }).map((item, index) => ({
    ...item,
    tone: (['accent-1', 'accent-2', 'accent-3'] as const)[index],
    sparkline:
      item.key === 'requests' ? sparklines?.requests : sparklines?.usage,
    daily: item.key === 'todayUsage',
  }))
  const tokenTotal = usageStats?.totals.tokens
  const fullTokenCount = formatNumber(tokenTotal, locale)
  const compactTokens = tokenTotal != null && fullTokenCount.length > 9
  let tokenDisplay = usagePlaceholder
  if (tokenTotal != null) {
    tokenDisplay = compactTokens
      ? formatCompactNumber(tokenTotal, locale)
      : fullTokenCount
  }
  items.unshift({
    key: 'tokens',
    title: t('Tokens (24h)'),
    value: tokenDisplay,
    description: `${t('Input tokens')} + ${t('Output tokens')}`,
    icon: Layers,
    tone: 'accent-2',
    sparkline: sparklines?.tokens,
    daily: true,
  })

  return (
    <section aria-label={t('Usage at a glance')}>
      <StaggerContainer className='grid grid-cols-2 gap-2 sm:gap-3 xl:grid-cols-5'>
        {items.map((item) => (
          <StaggerItem
            key={item.key}
            className='bg-card min-w-0 rounded-xl border p-3 sm:p-4'
          >
            <div
              role='group'
              aria-label={item.title}
              aria-busy={item.daily && usageTrendQuery.isLoading}
            >
              <StatCard
                title={item.title}
                value={item.value}
                exactValue={
                  item.key === 'tokens' && compactTokens
                    ? fullTokenCount
                    : undefined
                }
                description={item.description}
                icon={item.icon}
                tone={item.tone}
                sparkline={item.sparkline}
                sparklineVariant='line'
                showMobileSparkline={item.key === 'tokens'}
                loading={loading || (item.daily && usageTrendQuery.isLoading)}
                compactMobile
              />
            </div>
          </StaggerItem>
        ))}
        <StaggerItem className='bg-card col-span-2 min-w-0 rounded-xl border p-3 sm:p-4 xl:col-span-1'>
          <div
            role='group'
            aria-label={t('Credit remaining')}
            className='flex h-full flex-col gap-2'
          >
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <span className='text-muted-foreground text-xs font-medium'>
                {creditTitle}
              </span>
              <Button
                variant='ghost'
                size='sm'
                className='text-muted-foreground h-auto px-1 py-0.5 text-xs'
                render={<Link to='/wallet' />}
              >
                {hasSubscriptions ? t('My Subscriptions') : t('Wallet')}
                <ArrowRight data-icon='inline-end' />
              </Button>
            </div>
            {subscriptionsQuery.isLoading && (
              <LoadingState size='sm' className='min-h-20' />
            )}
            {subscriptionsQuery.isError && (
              <ErrorState
                className='min-h-20'
                onRetry={() => void subscriptionsQuery.refetch()}
              />
            )}
            {!subscriptionsQuery.isLoading && !subscriptionsQuery.isError && (
              <>
                <div className='flex flex-wrap items-baseline gap-x-2 gap-y-1'>
                  <strong className='text-xl font-semibold tracking-tight break-all tabular-nums sm:text-2xl'>
                    {creditValue}
                  </strong>
                  {creditStatus && (
                    <span className='text-muted-foreground inline-flex items-center gap-1.5 text-xs'>
                      <span
                        className={cn(
                          'size-1.5 shrink-0 rounded-full',
                          creditDotClass
                        )}
                        aria-hidden='true'
                      />
                      {creditStatus}
                    </span>
                  )}
                </div>
                <dl className='mt-auto grid gap-1.5 pt-1 text-xs'>
                  {hasSubscriptions && (
                    <div className='flex flex-wrap items-baseline justify-between gap-x-1'>
                      <dt className='text-muted-foreground'>
                        {t('Current period usage')}
                      </dt>
                      <dd className='font-medium tabular-nums'>
                        {formatQuota(subscriptionSummary.used)}
                      </dd>
                    </div>
                  )}
                  <div className='flex flex-wrap items-baseline justify-between gap-x-1'>
                    <dt className='text-muted-foreground inline-flex items-center gap-1'>
                      <TimingIcon
                        className='size-3 shrink-0'
                        aria-hidden='true'
                      />
                      {timingLabel}
                    </dt>
                    <dd
                      className={cn(
                        'font-medium tabular-nums',
                        !hasSubscriptions &&
                          healthLevel === 'critical' &&
                          'text-destructive',
                        !hasSubscriptions &&
                          healthLevel === 'caution' &&
                          'text-warning'
                      )}
                    >
                      {timingValue}
                    </dd>
                  </div>
                </dl>
              </>
            )}
          </div>
        </StaggerItem>
      </StaggerContainer>
    </section>
  )
}
