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
import { VChart } from '@visactor/react-vchart'
import { useEffect, useMemo, useState, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { useTheme } from '@/context/theme-provider'
import {
  DEFAULT_TIME_GRANULARITY,
  MODEL_ANALYTICS_CHART_OPTIONS,
  MODEL_METRIC_LABELS,
} from '@/features/dashboard/constants'
import { processChartData } from '@/features/dashboard/lib'
import type {
  DashboardChartTimeDomain,
  ModelDashboardMetric,
  ModelAnalyticsChartTab,
  QuotaDataItem,
} from '@/features/dashboard/types'
import { useThemeRadiusPx } from '@/lib/theme-radius'
import type { TimeGranularity } from '@/lib/time'
import { VCHART_OPTION } from '@/lib/vchart'

import { PanelWrapper } from '../ui/panel-wrapper'

let themeManagerPromise: Promise<
  (typeof import('@visactor/vchart'))['ThemeManager']
> | null = null

type ChartSpecKey = 'spec_model_line' | 'spec_pie' | 'spec_rank_bar'

const CHART_SPEC_KEYS: Record<ModelAnalyticsChartTab, ChartSpecKey> = {
  trend: 'spec_model_line',
  proportion: 'spec_pie',
  top: 'spec_rank_bar',
}

interface ModelChartsProps {
  metric?: ModelDashboardMetric
  data: QuotaDataItem[]
  loading?: boolean
  timeGranularity?: TimeGranularity
  trendOnly?: boolean
  defaultChartTab?: ModelAnalyticsChartTab
  timeDomain?: DashboardChartTimeDomain
  activeTab?: ModelAnalyticsChartTab
  onActiveTabChange?: (tab: ModelAnalyticsChartTab) => void
  title?: string
  description?: string
  headerActions?: ReactNode
}

export function ModelCharts(props: ModelChartsProps) {
  const { t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const { customization } = useThemeCustomization()
  const chartRadius = useThemeRadiusPx(
    '--radius-md',
    `${customization.preset}:${customization.radius}`
  )
  const [localTab, setLocalTab] = useState<ModelAnalyticsChartTab>(
    props.defaultChartTab ?? 'trend'
  )
  let activeTab = props.trendOnly ? 'trend' : (props.activeTab ?? localTab)
  // Shares have no coherent meaning for signed profit. Preserve the saved view for other metrics.
  if (props.metric === 'profit' && activeTab === 'proportion') activeTab = 'top'
  const title = props.title ?? t('Model Call Analytics')
  const [themeReady, setThemeReady] = useState(false)
  const themeManagerRef = useRef<
    (typeof import('@visactor/vchart'))['ThemeManager'] | null
  >(null)
  const timeGranularity = props.timeGranularity ?? DEFAULT_TIME_GRANULARITY

  useEffect(() => {
    if (props.defaultChartTab) setLocalTab(props.defaultChartTab)
  }, [props.defaultChartTab])

  useEffect(() => {
    const updateTheme = async () => {
      setThemeReady(false)

      if (!themeManagerPromise) {
        themeManagerPromise = import('@visactor/vchart').then(
          (m) => m.ThemeManager
        )
      }

      const ThemeManager = await themeManagerPromise
      themeManagerRef.current = ThemeManager
      ThemeManager.setCurrentTheme(resolvedTheme === 'dark' ? 'dark' : 'light')
      setThemeReady(true)
    }

    updateTheme()
  }, [resolvedTheme])

  const chartData = useMemo(
    () =>
      processChartData(
        props.loading ? [] : props.data,
        timeGranularity,
        t,
        chartRadius,
        props.timeDomain,
        props.metric,
        resolvedTheme === 'dark' ? 'dark' : 'light'
      ),
    [
      props.data,
      props.metric,
      resolvedTheme,
      props.loading,
      props.timeDomain,
      timeGranularity,
      t,
      chartRadius,
    ]
  )

  const spec = chartData[CHART_SPEC_KEYS[activeTab]]
  const specType = typeof spec?.type === 'string' ? spec.type : activeTab
  const chartKey = [
    activeTab,
    props.metric,
    specType,
    props.loading ? 'loading' : 'ready',
    props.data.length,
    resolvedTheme,
    customization.preset,
  ].join('-')

  return (
    <div role='group' aria-label={title} className='h-full min-w-0'>
      <PanelWrapper
        className='flex h-full flex-col'
        title={title}
        description={
          <span>
            {t(MODEL_METRIC_LABELS[props.metric ?? 'requests'])}{' '}
            <strong className='text-foreground tabular-nums'>
              {chartData.totalDisplay}
            </strong>
          </span>
        }
        contentClassName='flex flex-1 flex-col p-2 sm:p-3'
        headerActions={
          <div className='flex max-w-full flex-wrap items-center gap-2'>
            {!props.trendOnly && (
              <ToggleGroup
                value={[activeTab]}
                size='sm'
                aria-label={t('Chart type')}
                onValueChange={([value]) => {
                  const item = MODEL_ANALYTICS_CHART_OPTIONS.find(
                    (tab) => tab.value === value
                  )
                  if (!item) return
                  if (props.activeTab === undefined) setLocalTab(item.value)
                  props.onActiveTabChange?.(item.value)
                }}
              >
                <ToggleGroupItem value='trend'>{t('Trend')}</ToggleGroupItem>
                {props.metric !== 'profit' && (
                  <ToggleGroupItem value='proportion'>
                    {t('Distribution')}
                  </ToggleGroupItem>
                )}
                <ToggleGroupItem value='top'>{t('Ranking')}</ToggleGroupItem>
              </ToggleGroup>
            )}
            {props.headerActions}
          </div>
        }
      >
        <div className='relative min-h-56 flex-1 sm:min-h-64'>
          <div className='absolute inset-0'>
            {themeReady && spec && (
              <VChart
                key={chartKey}
                spec={{
                  ...spec,
                  title: { visible: false },
                  ...(props.trendOnly && { legends: { visible: false } }),
                  theme: resolvedTheme === 'dark' ? 'dark' : 'light',
                  background: 'transparent',
                }}
                option={VCHART_OPTION}
              />
            )}
          </div>
        </div>
        {props.description && (
          <p className='text-muted-foreground mt-1.5 text-xs'>
            {props.description}
          </p>
        )}
      </PanelWrapper>
    </div>
  )
}
