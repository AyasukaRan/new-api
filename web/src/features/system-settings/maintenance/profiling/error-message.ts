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

import { getServerErrorMessage } from '@/lib/server-error-message'

/** Translate the profiling API's fixed messages while preserving unknown errors. */
export function getProfilingErrorMessage(
  error: unknown,
  t: TFunction,
  fallback?: string
): string {
  const message = getServerErrorMessage(error, fallback)
  switch (message) {
    case 'Invalid profiling query':
      return t('Invalid profiling query')
    case 'Select a valid profile and a time range of at most 24 hours':
      return t('Select a valid profile and a time range of at most 24 hours')
    case 'Another profiling query is running':
      return t('Another profiling query is running')
    case 'Profiling service is unavailable':
      return t('Profiling service is unavailable')
    case 'Invalid profiling response':
      return t('Invalid profiling response')
    case 'Instant profiling is not enabled':
      return t('Instant profiling is not enabled')
    case 'Invalid profiling capture':
      return t('Invalid profiling capture')
    case 'CPU profiling is already provided by continuous profiling':
      return t('CPU profiling is already provided by continuous profiling')
    case 'Another profiling capture is running':
      return t('Another profiling capture is running')
    case 'Another CPU profiler is running':
      return t('Another CPU profiler is running')
    case 'Unable to capture profile':
      return t('Unable to capture profile')
    case 'Profile exceeded the size limit':
      return t('Profile exceeded the size limit')
    case 'Unable to read profile':
      return t('Unable to read profile')
    case 'Unable to store profile':
      return t('Unable to store profile')
    case 'Profile download has expired':
      return t('Profile download has expired')
    case 'Unable to download profile':
      return t('Unable to download profile')
    case 'Unable to load profiling data':
      return t('Unable to load profiling data')
    case 'Profile capture failed':
      return t('Profile capture failed')
    default:
      return message
  }
}
