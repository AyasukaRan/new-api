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
import { formatNumber } from '@/lib/format'

/** Profiling measurements retain their sampling unit; CPU time is not wall time. */
export function formatProfileValue(
  value: number,
  unit: string,
  locale?: string
): string {
  if (!Number.isFinite(value)) {
    return '—'
  }
  if (unit === 'nanoseconds') {
    if (Math.abs(value) >= 1e9) {
      return `${formatNumber(value / 1e9, locale)} s`
    }
    if (Math.abs(value) >= 1e6) {
      return `${formatNumber(value / 1e6, locale)} ms`
    }
    if (Math.abs(value) >= 1e3) {
      return `${formatNumber(value / 1e3, locale)} μs`
    }
    return `${formatNumber(value, locale)} ns`
  }
  if (unit === 'bytes') {
    if (Math.abs(value) >= 1024 ** 3) {
      return `${formatNumber(value / 1024 ** 3, locale)} GiB`
    }
    if (Math.abs(value) >= 1024 ** 2) {
      return `${formatNumber(value / 1024 ** 2, locale)} MiB`
    }
    if (Math.abs(value) >= 1024) {
      return `${formatNumber(value / 1024, locale)} KiB`
    }
    return `${formatNumber(value, locale)} B`
  }
  return formatNumber(value, locale)
}
