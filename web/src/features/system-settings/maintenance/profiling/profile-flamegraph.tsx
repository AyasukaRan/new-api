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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { ProfileFrame, ProfileResult } from './api'
import { formatProfileValue } from './profile-value'

const frameColors = [
  'bg-orange-200 text-orange-950 hover:bg-orange-300 dark:bg-orange-900 dark:text-orange-100 dark:hover:bg-orange-800',
  'bg-amber-200 text-amber-950 hover:bg-amber-300 dark:bg-amber-900 dark:text-amber-100 dark:hover:bg-amber-800',
  'bg-rose-200 text-rose-950 hover:bg-rose-300 dark:bg-rose-900 dark:text-rose-100 dark:hover:bg-rose-800',
]

export function ProfileFlamegraph(props: { profile: ProfileResult }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [focus, setFocus] = useState<ProfileFrame | null>(null)
  const start = focus?.start ?? 0
  const total = focus?.total ?? props.profile.total
  const depth = focus?.depth ?? 0
  const frames = useMemo(
    () =>
      props.profile.flamegraph
        .slice(0, 4096)
        .filter(
          (frame) =>
            Number.isFinite(frame.total) &&
            frame.total > 0 &&
            Number.isFinite(frame.start) &&
            Number.isInteger(frame.depth) &&
            frame.depth >= depth &&
            frame.start >= start &&
            frame.start < start + total &&
            frame.start + frame.total <= start + total + total * 1e-9
        ),
    [props.profile.flamegraph, depth, start, total]
  )
  const height =
    (frames.reduce((max, frame) => Math.max(max, frame.depth - depth), 0) + 1) *
    28

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground text-xs'>
          {t('Select a frame to zoom into its call stack.')}
        </p>
        <Button
          size='sm'
          variant='outline'
          disabled={!focus}
          onClick={() => setFocus(null)}
        >
          {t('Reset view')}
        </Button>
      </div>
      {focus && (
        <p className='text-sm break-all'>
          {t('Focused function')}:{' '}
          <span className='font-mono'>{focus.name}</span>
        </p>
      )}
      <div
        className='max-h-96 overflow-auto rounded-md border'
        role='region'
        aria-label={t('Flame graph')}
        tabIndex={0}
      >
        <div className='relative min-w-[640px]' style={{ height }}>
          {frames.map((frame) => {
            const value = formatProfileValue(
              frame.total,
              props.profile.unit,
              locale
            )
            const share = formatNumber(
              (frame.total / props.profile.total) * 100,
              locale
            )
            return (
              <Button
                key={`${frame.depth}:${frame.start}:${frame.name}`}
                variant='ghost'
                size='sm'
                className={cn(
                  'absolute h-[26px] min-w-0 justify-start overflow-hidden rounded-none border border-background/50 px-1 font-mono text-xs',
                  frameColors[frame.depth % frameColors.length]
                )}
                style={{
                  left: `${((frame.start - start) / total) * 100}%`,
                  width: `${Math.min((frame.total / total) * 100, 100)}%`,
                  top: (frame.depth - depth) * 28,
                }}
                title={`${frame.name} · ${value} · ${share}%`}
                aria-label={t('Zoom into {{name}} ({{value}}, {{share}}%)', {
                  name: frame.name,
                  value,
                  share,
                })}
                onClick={() => setFocus(frame)}
              >
                <span className='truncate'>{frame.name}</span>
              </Button>
            )
          })}
        </div>
      </div>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Frame width represents the selected metric. Color only separates stack levels.'
        )}
      </p>
    </div>
  )
}
