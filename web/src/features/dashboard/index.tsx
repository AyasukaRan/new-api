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
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { Eye, EyeOff } from 'lucide-react'
import { useState, useCallback, useMemo, lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { FadeIn } from '@/components/page-transition'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { computeTimeRange } from '@/lib/time'
import { useAuthStore } from '@/stores/auth-store'

import { getUserQuotaDates } from './api'
import { DashboardMetricSelector } from './components/models/dashboard-metric-selector'
import { ModelsChartPreferences } from './components/models/models-chart-preferences'
import { ModelsFilter } from './components/models/models-filter-dialog'
import { ModelRequestShare } from './components/overview/model-request-share'
import { OverviewDashboard } from './components/overview/overview-dashboard'
import { PanelWrapper } from './components/ui/panel-wrapper'
import {
  DEFAULT_TIME_GRANULARITY,
  MODEL_METRICS,
  TRAFFIC_METRICS,
} from './constants'
import { summarizeUsageRows } from './hooks/use-personal-usage'
import {
  buildDefaultDashboardFilters,
  buildQueryParams,
  getDefaultDays,
  getSavedChartPreferences,
  getSavedGranularity,
  saveChartPreferences,
} from './lib'
import { buildChartTimeDomain } from './lib/charts'
import {
  type DashboardSectionId,
  DASHBOARD_DEFAULT_SECTION,
  DASHBOARD_SECTION_IDS,
} from './section-registry'
import type {
  DashboardChartPreferences,
  ModelDashboardMetric,
  DashboardFilters,
  ModelAnalyticsChartTab,
  QuotaDataItem,
  UserChartsFilters,
} from './types'

const route = getRouteApi('/_authenticated/dashboard/$section')

const EMPTY_MODEL_DATA: QuotaDataItem[] = []

const LOG_STAT_CARD_FALLBACK_KEYS = [
  'count',
  'quota',
  'tokens',
  'cache-read',
  'cache-creation',
  'average-rpm',
  'average-tpm',
] as const
const LazyFinancialSummary = lazy(() =>
  import('./components/models/financial-summary').then((m) => ({
    default: m.FinancialSummary,
  }))
)

const LazyLogStatCards = lazy(() =>
  import('./components/models/log-stat-cards').then((m) => ({
    default: m.LogStatCards,
  }))
)

const LazyModelCharts = lazy(() =>
  import('./components/models/model-charts').then((m) => ({
    default: m.ModelCharts,
  }))
)

const LazyConsumptionDistributionChart = lazy(() =>
  import('./components/models/consumption-distribution-chart').then((m) => ({
    default: m.ConsumptionDistributionChart,
  }))
)

const LazyCallSources = lazy(() =>
  import('./components/models/call-sources').then((m) => ({
    default: m.CallSources,
  }))
)

const LazyGatewayDiagnostics = lazy(
  () => import('./components/overview/gateway-diagnostics')
)
const LazyUsageEfficiency = lazy(() =>
  import('./components/overview/usage-efficiency').then((m) => ({
    default: m.UsageEfficiency,
  }))
)

const LazyUserCharts = lazy(() =>
  import('./components/users/user-charts').then((m) => ({
    default: m.UserCharts,
  }))
)

const LazyFlowCharts = lazy(() =>
  import('./components/flow/flow-charts').then((m) => ({
    default: m.FlowCharts,
  }))
)

function LogStatCardsFallback() {
  return (
    <div className='bg-card grid grid-cols-2 gap-px rounded-xl border sm:grid-cols-4 xl:grid-cols-7'>
      {LOG_STAT_CARD_FALLBACK_KEYS.map((key) => (
        <div key={key} className='min-w-0 space-y-2 px-3 py-3'>
          <Skeleton className='h-3 w-16' />
          <Skeleton className='h-5 w-20' />
        </div>
      ))}
    </div>
  )
}

function ModelChartsFallback() {
  return (
    <div className='overflow-hidden rounded-lg border'>
      <div className='flex items-center justify-between border-b px-4 py-3 sm:px-5'>
        <Skeleton className='h-5 w-32' />
        <Skeleton className='h-8 w-72' />
      </div>
      <div className='h-60 p-2'>
        <Skeleton className='h-full w-full' />
      </div>
    </div>
  )
}

const SECTION_META: Record<DashboardSectionId, { titleKey: string }> = {
  overview: {
    titleKey: 'Overview',
  },
  models: {
    titleKey: 'Model Call Analytics',
  },
  flow: {
    titleKey: 'Flow',
  },
  users: {
    titleKey: 'User Analytics',
  },
}

export function Dashboard() {
  const { t, i18n } = useTranslation()
  const navigate = useNavigate()
  const params = route.useParams()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const userRole = useAuthStore((state) => state.auth.user?.role)
  const isAdmin = Boolean(userRole && userRole >= ROLE.ADMIN)
  const activeSection = (params.section ??
    DASHBOARD_DEFAULT_SECTION) as DashboardSectionId

  const [metric, setMetric] = useState<ModelDashboardMetric>('requests')
  const [chartPreferences, setChartPreferences] =
    useState<DashboardChartPreferences>(() => getSavedChartPreferences())
  const [analyticsTab, setAnalyticsTab] = useState<ModelAnalyticsChartTab>(
    () => chartPreferences.modelAnalyticsChart
  )
  const [modelFilters, setModelFilters] = useState<DashboardFilters>(() =>
    buildDefaultDashboardFilters(getSavedChartPreferences())
  )
  const [userChartsFilters, setUserChartsFilters] = useState<UserChartsFilters>(
    () => {
      const granularity = getSavedGranularity()
      return {
        timeGranularity: granularity,
        selectedRange: getDefaultDays(granularity),
        topUserLimit: 10,
      }
    }
  )
  const [flowSensitiveVisible, setFlowSensitiveVisible] = useState(true)
  const timeGranularity =
    modelFilters.time_granularity || DEFAULT_TIME_GRANULARITY
  const modelRange = useMemo(
    () =>
      computeTimeRange(
        getDefaultDays(timeGranularity),
        modelFilters.start_timestamp,
        modelFilters.end_timestamp
      ),
    [timeGranularity, modelFilters.start_timestamp, modelFilters.end_timestamp]
  )
  const chartTimeDomain = useMemo(
    () => buildChartTimeDomain(modelRange, timeGranularity),
    [modelRange, timeGranularity]
  )
  const modelParams = buildQueryParams(modelRange, {
    time_granularity: timeGranularity,
    username: isAdmin ? modelFilters.username : undefined,
  })
  const modelQuery = useQuery({
    queryKey: ['dashboard', 'model-analytics', userId, isAdmin, modelParams],
    enabled: activeSection === 'models' && userId != null,
    staleTime: 60_000,
    queryFn: async () => {
      const response = requireServerSuccess(
        await getUserQuotaDates(modelParams, isAdmin)
      )
      if (response.data !== null && !Array.isArray(response.data)) {
        throw new Error('Invalid response')
      }
      return response.data ?? []
    },
  })
  const periodRows = modelQuery.data ?? EMPTY_MODEL_DATA
  const periodUsage = useMemo(
    () => chartTimeDomain && summarizeUsageRows(periodRows, chartTimeDomain),
    [periodRows, chartTimeDomain]
  )
  const modelData = periodUsage?.rows ?? periodRows
  const dataLoading = modelQuery.isPending
  const handleFilterChange = useCallback((filters: DashboardFilters) => {
    setModelFilters(filters)
  }, [])

  const handleResetFilters = useCallback(() => {
    setModelFilters(buildDefaultDashboardFilters(chartPreferences))
  }, [chartPreferences])

  const handleChartPreferencesChange = useCallback(
    (preferences: DashboardChartPreferences) => {
      setChartPreferences(preferences)
      setAnalyticsTab(preferences.modelAnalyticsChart)
      setModelFilters(buildDefaultDashboardFilters(preferences))
      saveChartPreferences(preferences)
    },
    []
  )

  const meta = SECTION_META[activeSection] ?? SECTION_META.overview
  const effectiveMetric = isAdmin || metric === 'tokens' ? metric : 'requests'
  const shareMetric =
    effectiveMetric === 'requests' || effectiveMetric === 'tokens'
      ? effectiveMetric
      : undefined
  const showRequestShare = shareMetric !== undefined && analyticsTab === 'trend'
  const rangeFormat = new Intl.DateTimeFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }
  )
  const visibleSections = useMemo(
    () =>
      DASHBOARD_SECTION_IDS.filter(
        (section) => section !== 'overview' && (section !== 'users' || isAdmin)
      ),
    [isAdmin]
  )
  const handleSectionChange = useCallback(
    (section: string) => {
      void navigate({
        to: '/dashboard/$section',
        params: { section: section as DashboardSectionId },
      })
    },
    [navigate]
  )
  const showSectionTabs =
    activeSection !== 'overview' && visibleSections.length > 1
  const modelActions =
    activeSection === 'models' ? (
      <>
        <ModelsChartPreferences
          preferences={chartPreferences}
          onPreferencesChange={handleChartPreferencesChange}
        />
        <ModelsFilter
          preferences={chartPreferences}
          currentFilters={modelFilters}
          onFilterChange={handleFilterChange}
          onReset={handleResetFilters}
        />
      </>
    ) : null
  const flowActions =
    activeSection === 'flow' ? (
      <>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                onClick={() => setFlowSensitiveVisible((prev) => !prev)}
                aria-label={
                  flowSensitiveVisible
                    ? t('Hide sensitive data')
                    : t('Show sensitive data')
                }
                className='text-muted-foreground hover:text-foreground size-8'
              />
            }
          >
            {flowSensitiveVisible ? <Eye /> : <EyeOff />}
          </TooltipTrigger>
          <TooltipContent>
            {flowSensitiveVisible
              ? t('Hide sensitive data')
              : t('Show sensitive data')}
          </TooltipContent>
        </Tooltip>
        <ModelsFilter
          preferences={chartPreferences}
          currentFilters={modelFilters}
          onFilterChange={handleFilterChange}
          onReset={handleResetFilters}
          titleKey='Flow Filters'
          descriptionKey='Filter the traffic flow view by time range and user.'
        />
      </>
    ) : null
  const sectionActions = modelActions ?? flowActions

  if (activeSection === 'overview' || (activeSection === 'users' && !isAdmin)) {
    return <OverviewDashboard />
  }

  return (
    <SectionPageLayout className='dashboard-workbench'>
      <SectionPageLayout.Title>{t(meta.titleKey)}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-3 sm:space-y-4'>
          <div className='flex flex-wrap items-center justify-between gap-1.5 sm:gap-2'>
            {showSectionTabs ? (
              <Tabs value={activeSection} onValueChange={handleSectionChange}>
                <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
                  {visibleSections.map((section) => (
                    <TabsTrigger key={section} value={section}>
                      {t(SECTION_META[section].titleKey)}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
            ) : (
              <div />
            )}
            {sectionActions != null && (
              <div className='flex shrink-0 flex-wrap items-center gap-1.5 sm:gap-2'>
                {sectionActions}
              </div>
            )}
          </div>
          {activeSection === 'models' && (
            <>
              <FadeIn>
                <Suspense fallback={<LogStatCardsFallback />}>
                  <LazyLogStatCards
                    data={modelData}
                    loading={dataLoading}
                    error={modelQuery.isError}
                    timeRangeMinutes={
                      (modelRange.end_timestamp - modelRange.start_timestamp) /
                      60
                    }
                  />
                </Suspense>
              </FadeIn>
              <div className='flex flex-wrap items-center justify-between gap-x-4 gap-y-2'>
                <DashboardMetricSelector
                  value={effectiveMetric}
                  onChange={setMetric}
                  options={isAdmin ? MODEL_METRICS : TRAFFIC_METRICS}
                />
                <div
                  aria-label={t('Time range')}
                  className='text-muted-foreground text-xs tabular-nums'
                >
                  {isAdmin && modelFilters.username && (
                    <span className='text-foreground mr-2 font-medium'>
                      {modelFilters.username}
                    </span>
                  )}
                  {rangeFormat.format(modelRange.start_timestamp * 1000)} –{' '}
                  {rangeFormat.format(modelRange.end_timestamp * 1000)}
                </div>
              </div>
              <div className='grid min-w-0 grid-cols-1 items-stretch gap-3 lg:grid-cols-2'>
                {modelQuery.isError && (
                  <PanelWrapper title={t('Model Call Analytics')}>
                    <ErrorState
                      className='min-h-48'
                      title={t('Failed to fetch usage')}
                      onRetry={() => void modelQuery.refetch()}
                    />
                  </PanelWrapper>
                )}
                {!modelQuery.isError &&
                  (dataLoading || modelData.length === 0) && (
                    <PanelWrapper
                      title={t('Model Call Analytics')}
                      loading={dataLoading}
                      empty
                    />
                  )}
                {modelQuery.isSuccess && modelData.length > 0 && (
                  <Suspense fallback={<ModelChartsFallback />}>
                    <LazyModelCharts
                      metric={effectiveMetric}
                      data={modelData}
                      timeDomain={chartTimeDomain}
                      activeTab={analyticsTab}
                      onActiveTabChange={setAnalyticsTab}
                      timeGranularity={timeGranularity}
                    />
                  </Suspense>
                )}
                {showRequestShare &&
                  !modelQuery.isError &&
                  modelData.length > 0 && (
                    <ModelRequestShare data={modelData} metric={shareMetric} />
                  )}
                <Suspense fallback={<ModelChartsFallback />}>
                  <LazyCallSources
                    metric={effectiveMetric}
                    filters={modelFilters}
                    timeDomain={chartTimeDomain}
                    activeTab={analyticsTab}
                    onActiveTabChange={setAnalyticsTab}
                  />
                </Suspense>
                {modelQuery.isSuccess && modelData.length > 0 && (
                  <Suspense fallback={<ModelChartsFallback />}>
                    <LazyConsumptionDistributionChart
                      metric={effectiveMetric}
                      data={modelData}
                      timeDomain={chartTimeDomain}
                      defaultChartType={
                        chartPreferences.consumptionDistributionChart
                      }
                      timeGranularity={timeGranularity}
                    />
                  </Suspense>
                )}
                {modelQuery.isSuccess &&
                  periodUsage &&
                  periodUsage.rows.length > 0 && (
                    <Suspense fallback={<ModelChartsFallback />}>
                      <LazyUsageEfficiency
                        data={periodUsage}
                        granularity={timeGranularity}
                      />
                    </Suspense>
                  )}
                {isAdmin && (
                  <Suspense fallback={<ModelChartsFallback />}>
                    <LazyFinancialSummary
                      data={modelData}
                      loading={dataLoading}
                      error={modelQuery.isError}
                    />
                  </Suspense>
                )}
              </div>
              {isAdmin && !modelFilters.username?.trim() && (
                <Suspense fallback={<ModelChartsFallback />}>
                  <LazyGatewayDiagnostics />
                </Suspense>
              )}
            </>
          )}
          {activeSection === 'users' && (
            <FadeIn>
              <Suspense fallback={<ModelChartsFallback />}>
                <LazyUserCharts
                  filters={userChartsFilters}
                  onFiltersChange={setUserChartsFilters}
                />
              </Suspense>
            </FadeIn>
          )}
          {activeSection === 'flow' && (
            <FadeIn>
              <Suspense fallback={<ModelChartsFallback />}>
                <LazyFlowCharts
                  filters={modelFilters}
                  sensitiveVisible={flowSensitiveVisible}
                />
              </Suspense>
            </FadeIn>
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
