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
import type { TFunction } from 'i18next'
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Gauge,
  HardDriveDownload,
  HardDriveUpload,
  Receipt,
  Target,
  TicketPercent,
  Timer,
  TrendingUp,
  Zap,
} from 'lucide-react'

export const metricIcons = {
  'Total input tokens': ArrowDownToLine,
  'Output Tokens': ArrowUpFromLine,
  'Cache read tokens': HardDriveDownload,
  'Cache write tokens': HardDriveUpload,
  'Cache hit rate': Target,
  'Time to first token': Zap,
  'Total time': Timer,
  'Output speed': Gauge,
  'Upstream cost': Receipt,
  'Gross profit': TrendingUp,
  'Free allowance deduction': TicketPercent,
} as const

type LogMetric = keyof typeof metricIcons

export function renderLogMetricTerm(metric: LogMetric, t: TFunction) {
  const Icon = metricIcons[metric]
  const label = t(metric)
  return (
    <dt
      className='text-muted-foreground flex items-center'
      title={label}
      aria-label={label}
    >
      <Icon
        aria-hidden='true'
        className='size-3.5 shrink-0'
        strokeWidth={1.75}
      />
      <span className='sr-only'>{label}</span>
    </dt>
  )
}
