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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toIntlLocale } from '@/i18n/languages'
import { formatTimestampToDate } from '@/lib/format'

import { formatChannelBalanceAmount } from '../lib/channel-balance'
import { getKeyBalanceHistory } from '../lib/channel-key-monitoring'
import type { ChannelBalanceSample } from '../types'

export function ChannelKeyBalanceHistory(props: {
  history: ChannelBalanceSample[]
  balanceRechargeMultiplier?: number
  currency?: string
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const id = useId()
  const indices = [
    ...new Set(
      props.history.flatMap((sample) =>
        sample.key_balances.map((key) => key.index)
      )
    ),
  ].sort((a, b) => a - b)
  const [selected, setSelected] = useState<number | null>(null)
  const index =
    selected !== null && indices.includes(selected)
      ? selected
      : (indices[0] ?? 0)
  const options = indices.map((value) => ({
    value: String(value),
    label: t('Key #{{index}}', { index: value + 1 }),
  }))
  const points = getKeyBalanceHistory(
    props.history,
    index,
    props.balanceRechargeMultiplier
  )
  const color = `var(--chart-${(index % 5) + 1})`
  return (
    <div className='space-y-3'>
      <Field className='max-w-64'>
        <FieldLabel htmlFor={id}>{t('Balance history key')}</FieldLabel>
        <Select
          items={options}
          value={String(index)}
          onValueChange={(value) => {
            if (value !== null) setSelected(Number(value))
          }}
        >
          <SelectTrigger id={id} className='w-full'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <ChartContainer
        className='h-52 w-full'
        config={{ balance: { label: t('Balance'), color } }}
      >
        <LineChart accessibilityLayer data={points}>
          <CartesianGrid vertical={false} />
          <XAxis
            dataKey='timestamp'
            type='number'
            domain={['dataMin', 'dataMax']}
            tickFormatter={(value: number) => formatTimestampToDate(value)}
            minTickGap={60}
            tickLine={false}
          />
          <YAxis
            width={85}
            tickFormatter={(value: number) =>
              formatChannelBalanceAmount(value, props.currency, {
                locale,
                compact: true,
              })
            }
          />
          <ChartTooltip
            content={
              <ChartTooltipContent
                labelFormatter={(_, payload) =>
                  formatTimestampToDate(Number(payload[0]?.payload?.timestamp))
                }
                formatter={(value) =>
                  formatChannelBalanceAmount(Number(value), props.currency, {
                    locale,
                    digitsSmall: 6,
                    abbreviate: false,
                  })
                }
              />
            }
          />
          <Line
            type='monotone'
            dataKey='balance'
            stroke='var(--color-balance)'
            strokeWidth={2}
            dot={{ r: 3 }}
            connectNulls={false}
            isAnimationActive={false}
          />
        </LineChart>
      </ChartContainer>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Failed queries and account changes break the balance curve. Shared account balances do not represent usage by an individual key.'
        )}
      </p>
    </div>
  )
}
