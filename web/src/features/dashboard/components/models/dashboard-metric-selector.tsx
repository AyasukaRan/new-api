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
import { useTranslation } from 'react-i18next'

import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { DashboardMetric } from '@/features/dashboard/types'

interface DashboardMetricSelectorProps {
  value: DashboardMetric
  onChange: (metric: DashboardMetric) => void
  includeRequests?: boolean
}

export function DashboardMetricSelector(props: DashboardMetricSelectorProps) {
  const { t } = useTranslation()
  const items = [
    ...(props.includeRequests
      ? [{ value: 'requests' as const, label: t('Requests') }]
      : []),
    { value: 'quota' as const, label: t('Revenue') },
    { value: 'cost' as const, label: t('Upstream cost') },
    { value: 'profit' as const, label: t('Gross profit') },
  ]
  return (
    <Tabs
      value={props.value}
      onValueChange={(value) => props.onChange(value as DashboardMetric)}
    >
      <TabsList
        aria-label={t('Chart metric')}
        className='max-w-full flex-wrap group-data-horizontal/tabs:h-auto'
      >
        {items.map((item) => (
          <TabsTrigger key={item.value} value={item.value}>
            {item.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
