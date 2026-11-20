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
import { MODEL_METRIC_LABELS } from '@/features/dashboard/constants'
import type { ModelDashboardMetric } from '@/features/dashboard/types'

interface DashboardMetricSelectorProps<M extends ModelDashboardMetric> {
  value: M
  options: readonly M[]
  onChange: (metric: M) => void
}

export function DashboardMetricSelector<M extends ModelDashboardMetric>(
  props: DashboardMetricSelectorProps<M>
) {
  const { t } = useTranslation()
  return (
    <Tabs
      value={props.value}
      onValueChange={(value) => {
        const metric = props.options.find((option) => option === value)
        if (metric) props.onChange(metric)
      }}
    >
      <TabsList
        aria-label={t('Chart metric')}
        className='max-w-full flex-wrap group-data-horizontal/tabs:h-auto'
      >
        {props.options.map((metric) => (
          <TabsTrigger key={metric} value={metric}>
            {t(MODEL_METRIC_LABELS[metric])}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
