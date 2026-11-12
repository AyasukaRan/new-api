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
import dayjs from '@/lib/dayjs'

export const LOG_TIME_PRESETS = ['today', '7d', 'week', '30d', 'month'] as const

export type LogTimePreset = (typeof LOG_TIME_PRESETS)[number]

export function getLogTimePreset(
  search: Record<string, unknown>
): LogTimePreset | undefined {
  const preset = LOG_TIME_PRESETS.find((value) => value === search.timeRange)
  if (preset) return preset
  if (search.startTime == null && search.endTime == null) return 'today'
  return undefined
}

export function getRelativeLogTimeRange(
  preset: LogTimePreset,
  now: Date = new Date()
): { start: Date; end: Date } {
  const current = dayjs(now)
  let start = current.startOf('day')
  switch (preset) {
    case '7d':
      start = current.subtract(6, 'day').startOf('day')
      break
    case 'week':
      start = current.startOf('week')
      break
    case '30d':
      start = current.subtract(29, 'day').startOf('day')
      break
    case 'month':
      start = current.startOf('month')
      break
  }
  return { start: start.toDate(), end: current.toDate() }
}
