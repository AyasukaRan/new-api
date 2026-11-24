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
import { useState, type ReactElement, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatTimestampToDate } from '@/lib/format'

import { LOG_TYPE_ENUM } from '../constants'
import type { UsageLog } from '../data/schema'
import { isViolationFeeLog } from '../lib/format'
import { isTimingLogType } from '../lib/utils'
import type { LogOtherData } from '../types'
import { LogChargeDetails } from './log-charge-details'
import { LogRequestOverview } from './log-request-overview'
import { LogTimingDetails } from './log-timing-details'

type RequestMetricProps = {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
}

export function LogRequestDetailTabs(
  props: RequestMetricProps & {
    initialTab: 'overview' | 'cost' | 'timing' | 'technical'
    children?: ReactNode
  }
) {
  const { t } = useTranslation()
  const charge = (
    <LogChargeDetails
      log={props.log}
      other={props.other}
      isAdmin={
        props.isAdmin &&
        props.log.type === LOG_TYPE_ENUM.CONSUME &&
        !isViolationFeeLog(props.other)
      }
    />
  )
  if (!isTimingLogType(props.log.type)) return charge
  const hasCharge =
    props.log.type !== LOG_TYPE_ENUM.ERROR ||
    props.log.quota > 0 ||
    props.initialTab === 'cost'
  return (
    <Tabs defaultValue={props.initialTab} className='gap-3'>
      <div className='border-border bg-popover sticky top-0 z-10 overflow-x-auto border-b'>
        <TabsList
          variant='line'
          aria-label={t('Request details')}
          className='gap-1 sm:gap-3'
        >
          <TabsTrigger value='overview'>{t('Overview')}</TabsTrigger>
          {hasCharge && (
            <TabsTrigger value='cost'>{t('Cost breakdown')}</TabsTrigger>
          )}
          <TabsTrigger value='timing'>{t('Timing breakdown')}</TabsTrigger>
          {props.children && (
            <TabsTrigger value='technical'>
              {t('Technical details')}
            </TabsTrigger>
          )}
        </TabsList>
      </div>
      <TabsContent value='overview'>
        <LogRequestOverview
          log={props.log}
          other={props.other}
          isAdmin={props.isAdmin}
        />
      </TabsContent>
      {hasCharge && (
        <TabsContent value='cost' className='mx-auto w-full max-w-xl'>
          {charge}
        </TabsContent>
      )}
      <TabsContent value='timing' className='mx-auto w-full max-w-xl'>
        <LogTimingDetails
          log={props.log}
          other={props.other}
          isAdmin={props.isAdmin}
        />
      </TabsContent>
      {props.children && (
        <TabsContent value='technical'>{props.children}</TabsContent>
      )}
    </Tabs>
  )
}

export function LogMetricsDetailDialog(
  props: RequestMetricProps & {
    initialTab: 'cost' | 'timing'
    trigger: ReactElement
  }
) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <Dialog
      open={open}
      onOpenChange={setOpen}
      trigger={props.trigger}
      title={t('Request details')}
      description={[
        props.log.model_name,
        formatTimestampToDate(props.log.created_at),
      ]
        .filter(Boolean)
        .join(' · ')}
      descriptionClassName='break-all font-mono text-xs tabular-nums'
      contentClassName='max-h-[min(88dvh,var(--dialog-available-height))] gap-3 sm:max-w-xl sm:has-[[data-slot=log-request-overview]]:max-w-[min(96vw,64rem)]'
    >
      {open && (
        <LogRequestDetailTabs
          log={props.log}
          other={props.other}
          isAdmin={props.isAdmin}
          initialTab={props.initialTab}
        />
      )}
    </Dialog>
  )
}
