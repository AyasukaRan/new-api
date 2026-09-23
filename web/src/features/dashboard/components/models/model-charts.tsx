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
import { PieChart as PieChartIcon } from 'lucide-react'
import { useEffect, useMemo, useState, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { useTheme } from '@/context/theme-provider'
import {
  DEFAULT_TIME_GRANULARITY,
  MODEL_ANALYTICS_CHART_OPTIONS,
} from '@/features/dashboard/constants'
import { processChartData } from '@/features/dashboard/lib'
import type {
  DashboardChartTimeDomain,
  ModelAnalyticsChartTab,
  QuotaDataItem,
} from '@/features/dashboard/types'
import { useThemeRadiusPx } from '@/lib/theme-radius'
import type { TimeGranularity } from '@/lib/time'
import { VCHART_OPTION } from '@/lib/vchart'

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
  data: QuotaDataItem[]
  loading?: boolean
  timeGranularity?: TimeGranularity
  defaultChartTab?: ModelAnalyticsChartTab
  timeDomain?: DashboardChartTimeDomain
  activeTab?: ModelAnalyticsChartTab
  onActiveTabChange?: (tab: ModelAnalyticsChartTab) => void
  title?: string
  description?: string
  headerActions?: ReactNode
  children?: ReactNode
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
  const activeTab = props.activeTab ?? localTab
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
        props.timeDomain
      ),
    [
      props.data,
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
    specType,
    props.loading ? 'loading' : 'ready',
    props.data.length,
    resolvedTheme,
    customization.preset,
  ].join('-')

  return (
    <div
      role='group'
      aria-label={title}
      className='overflow-hidden rounded-lg border'
    >
      <div className='flex w-full flex-col gap-1.5 border-b px-3 py-2 sm:gap-3 sm:px-5 sm:py-3 lg:flex-row lg:items-center lg:justify-between'>
        <div className='min-w-0 space-y-1'>
          <div className='flex items-center gap-2'>
            <IconBadge tone='chart-4' size='sm'>
              <PieChartIcon />
            </IconBadge>
            <div className='text-sm font-semibold'>{title}</div>
            <span className='text-muted-foreground text-xs'>
              {t('Total:')} {chartData.totalCountDisplay}
            </span>
          </div>
          {props.description && (
            <p className='text-muted-foreground text-xs'>{props.description}</p>
          )}
        </div>

        <div className='flex min-w-0 flex-wrap items-center gap-2 lg:justify-end'>
          <div className='bg-muted/60 inline-flex max-w-full overflow-x-auto rounded-lg border p-0.5'>
            {MODEL_ANALYTICS_CHART_OPTIONS.map((tab) => (
              <Button
                key={tab.value}
                type='button'
                variant='ghost'
                size='sm'
                aria-pressed={activeTab === tab.value}
                onClick={() => {
                  if (props.activeTab === undefined) setLocalTab(tab.value)
                  props.onActiveTabChange?.(tab.value)
                }}
                className={`shrink-0 rounded-md px-3 text-xs font-medium transition-colors ${
                  activeTab === tab.value
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {t(tab.labelKey)}
              </Button>
            ))}
          </div>
          {props.headerActions}
        </div>
      </div>

      <div className='h-[300px] p-1.5 sm:h-96 sm:p-2'>
        {themeReady && spec && (
          <VChart
            key={chartKey}
            spec={{
              ...spec,
              theme: resolvedTheme === 'dark' ? 'dark' : 'light',
              background: 'transparent',
            }}
            option={VCHART_OPTION}
          />
        )}
      </div>
      {props.children && (
        <div className='border-t p-4 sm:p-5'>{props.children}</div>
      )}
    </div>
  )
}
