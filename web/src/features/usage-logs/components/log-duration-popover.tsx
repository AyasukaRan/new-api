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

import { Button } from '@/components/ui/button'
import { formatUseTime } from '@/lib/format'

import type { UsageLog } from '../data/schema'
import { formatFirstResponseTime } from '../lib/format'
import type { LogOtherData } from '../types'
import { LogMetricsDetailDialog } from './log-metrics-detail-dialog'

export function LogDurationPopover(props: {
  log: UsageLog
  other: LogOtherData | null
  isAdmin: boolean
}) {
  const { t } = useTranslation()
  const exact = props.other?.duration_ms
  let duration =
    props.log.use_time === 0 ? '<1s' : formatUseTime(props.log.use_time)
  if (exact != null && Number.isFinite(exact) && exact >= 0) {
    duration = formatFirstResponseTime(exact)
  }

  return (
    <LogMetricsDetailDialog
      {...props}
      initialTab='timing'
      trigger={
        <Button
          variant='ghost'
          aria-label={`${t('Timing breakdown')} · ${duration}`}
          onClick={(event) => event.stopPropagation()}
          className='hover:text-primary decoration-muted-foreground/65 h-auto min-h-4 rounded-xs px-0 py-0 font-mono text-xs font-semibold text-inherit underline decoration-dotted underline-offset-4 hover:bg-transparent'
        >
          {duration}
        </Button>
      }
    />
  )
}
