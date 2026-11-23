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
import { useState, type ReactElement } from 'react'
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
import { LogTimingDetails } from './log-timing-details'

export function LogMetricsDetailDialog(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
  initialTab: 'cost' | 'timing'
  trigger: ReactElement
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const hasTiming = isTimingLogType(props.log.type)
  const charge = open && (
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
      contentClassName='max-h-[min(46.25rem,calc(100dvh-1rem))] gap-2.5 sm:max-w-[33.75rem]'
    >
      {open &&
        (hasTiming ? (
          <Tabs defaultValue={props.initialTab} className='gap-4'>
            <div className='border-border bg-popover sticky top-0 z-10 border-b'>
              <TabsList
                variant='line'
                aria-label={t('Request details')}
                className='gap-4'
              >
                <TabsTrigger value='cost'>{t('Cost breakdown')}</TabsTrigger>
                <TabsTrigger value='timing'>
                  {t('Timing breakdown')}
                </TabsTrigger>
              </TabsList>
            </div>
            <TabsContent value='cost'>{charge}</TabsContent>
            <TabsContent value='timing'>
              <LogTimingDetails
                log={props.log}
                other={props.other}
                isAdmin={props.isAdmin}
              />
            </TabsContent>
          </Tabs>
        ) : (
          charge
        ))}
    </Dialog>
  )
}
