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

import offPeakImage from '@/assets/billing-tiers/off-peak.png'
import peakImage from '@/assets/billing-tiers/peak.png'
import standardImage from '@/assets/billing-tiers/standard.png'
import { StatusBadge } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import styles from './billing-tier-icon.module.css'

export type BillingTier = 'peak' | 'off_peak' | 'standard'

const tierImages: Record<BillingTier, string> = {
  peak: peakImage,
  off_peak: offPeakImage,
  standard: standardImage,
}

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
      <img
        src={tierImages[props.tier]}
        alt=''
        aria-hidden='true'
        width={32}
        height={28}
        decoding='async'
        draggable={false}
        className={cn('h-full w-full', styles.artwork)}
      />
    </StatusBadge>
  )
}
