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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

import { OverviewFinancialSummary } from '../models/financial-summary'
import {
  GatewayModelHotspotsPanel,
  PerformanceHealthPanel,
  ResponsePerformancePanel,
} from './performance-health-panel'

export default function GatewayDiagnostics(props: { overview?: boolean }) {
  const { t } = useTranslation()
  const [hours, setHours] = useState<24 | 168>(24)
  const title = t('Gateway activity')

  return (
    <section className='space-y-2' aria-label={title}>
      {props.overview && (
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <h3 className='text-sm font-semibold'>{title}</h3>
          <ToggleGroup
            size='sm'
            variant='outline'
            value={[String(hours)]}
            aria-label={t('Gateway monitoring period')}
            onValueChange={([value]) => {
              if (value === '24' || value === '168') {
                setHours(value === '24' ? 24 : 168)
              }
            }}
          >
            <ToggleGroupItem value='24'>{t('Last 24 hours')}</ToggleGroupItem>
            <ToggleGroupItem value='168'>{t('Last 7 days')}</ToggleGroupItem>
          </ToggleGroup>
        </div>
      )}
      <div className='grid grid-cols-1 items-stretch gap-3 lg:grid-cols-2'>
        <PerformanceHealthPanel hours={hours} timelineOnly={props.overview} />
        {props.overview && <GatewayModelHotspotsPanel hours={hours} />}
        <ResponsePerformancePanel hours={hours} />
        {props.overview && <OverviewFinancialSummary hours={hours} />}
      </div>
    </section>
  )
}
