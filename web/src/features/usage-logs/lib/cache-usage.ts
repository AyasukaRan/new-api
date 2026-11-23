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
import type { LogOtherData } from '../types'

function count(value?: number): number | null {
  return value != null && Number.isFinite(value) && value >= 0 ? value : null
}

export function getLogCacheUsage(
  promptTokens: number,
  other: LogOtherData | null
) {
  const read = count(other?.cache_tokens)
  const shortWrite = count(other?.cache_creation_tokens_5m)
  const longWrite = count(other?.cache_creation_tokens_1h)
  const splitWrite =
    shortWrite == null && longWrite == null
      ? null
      : (shortWrite ?? 0) + (longWrite ?? 0)
  const writes = [
    count(other?.cache_write_tokens),
    count(other?.cache_creation_tokens),
    splitWrite,
  ].filter((value): value is number => value != null)
  // Twelve-hour tokens are a subset of cache writes; never add them again.
  const write = writes.length ? Math.max(...writes) : null
  let total = count(other?.input_tokens_total)
  if (total == null || total === 0) {
    total = count(promptTokens)
    if (
      total != null &&
      (other?.usage_semantic === 'anthropic' ||
        (!other?.usage_semantic && (other?.claude || (splitWrite ?? 0) > 0)))
    ) {
      total += (read ?? 0) + (write ?? 0)
    }
  }
  const percent =
    total != null &&
    total > 0 &&
    read != null &&
    read <= total &&
    count(promptTokens) != null
      ? (100 * read) / total
      : null
  return { total, read, write, percent }
}
