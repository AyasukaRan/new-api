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
import { Layers } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { LongContextBilling } from '../lib/long-context-billing'

export function LongContextBillingTag(props: {
  billing: LongContextBilling | null
}) {
  const { t, i18n } = useTranslation()
  if (!props.billing) return null
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <Tooltip>
      <TooltipTrigger
        render={<span role='note' tabIndex={0} />}
        aria-label={t('Long context')}
        className='focus-visible:ring-ring inline-flex size-4 shrink-0 cursor-help items-center justify-center rounded-sm text-amber-700 hover:text-amber-800 focus-visible:text-amber-800 focus-visible:ring-2 focus-visible:outline-none dark:text-amber-400/85 dark:hover:text-amber-300 dark:focus-visible:text-amber-300'
      >
        <Layers className='size-3.5' aria-hidden='true' />
      </TooltipTrigger>
      <TooltipContent role='tooltip' className='max-w-xs'>
        <dl className='grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1'>
          <dt>{t('Context threshold')}</dt>
          <dd className='text-end font-mono tabular-nums'>
            {props.billing.inclusive ? '≥' : '>'}{' '}
            {formatNumber(props.billing.threshold, locale)}
          </dd>
          <dt>{t('Billed input (including cache)')}</dt>
          <dd className='text-end font-mono tabular-nums'>
            {formatNumber(props.billing.inputTokens, locale)}
          </dd>
          <dt>{t('Pricing tier')}</dt>
          <dd className='max-w-36 text-end font-mono break-words'>
            {props.billing.tier}
          </dd>
        </dl>
      </TooltipContent>
    </Tooltip>
  )
}
