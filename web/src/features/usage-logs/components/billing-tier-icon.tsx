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
import { StatusBadge } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import styles from './billing-tier-icon.module.css'

export type BillingTier = 'peak' | 'off_peak' | 'standard'

export function BillingTierIcon(props: { tier: BillingTier; label: string }) {
  return (
    <StatusBadge
      type='badge'
      copyable={false}
      role='img'
      aria-label={props.label}
      data-tier={props.tier}
      className={cn(
        'mr-1.5 h-7 w-8 justify-center rounded-md p-0 align-middle',
        styles.scene
      )}
    >
      <svg
        width='32'
        height='28'
        viewBox='0 0 32 28'
        aria-hidden='true'
        focusable='false'
      >
        {props.tier === 'peak' && (
          <>
            <circle className={styles.sun} cx='25' cy='6' r='3.2' />
            <g className={styles.summit}>
              <path className={styles.farSlope} d='M1.5 24 10 11.5 19 24Z' />
              <path
                className={styles.nearSlope}
                d='M7 24 17.4 5.5Q18 4.5 18.6 5.5L30.5 24Z'
              />
              <path
                className={styles.snow}
                d='m13 13.4 4.4-7.9q.6-1 1.2 0l5.2 8.1-3.2-1.9-2.5 3-2.4-2.6Z'
              />
              <path className={styles.ridge} d='M2 24.5h28' />
            </g>
          </>
        )}
        {props.tier === 'off_peak' && (
          <>
            <path className={styles.valleySky} d='M5 9q11-6 22 0v15H5Z' />
            <path className={styles.farSlope} d='M1.5 11q3-3 6 1l8 12H1.5Z' />
            <path
              className={styles.nearSlope}
              d='M30.5 10q-3-3-6 2l-7 12h13Z'
            />
            <path className={styles.stream} d='M16 15.5c-7 3.2 9 5 0 9' />
          </>
        )}
        {props.tier === 'standard' && (
          <g className={styles.horizon}>
            <path className={styles.sun} d='M19 12.5a4.2 4.2 0 0 1 8.4 0Z' />
            <rect
              className={styles.flatland}
              x='1.5'
              y='12.5'
              width='29'
              height='7'
              rx='2.5'
            />
            <path className={styles.ridge} d='M2 12.5h28' />
            <path className={styles.field} d='M6 16h9m5 0h6' />
          </g>
        )}
      </svg>
    </StatusBadge>
  )
}
