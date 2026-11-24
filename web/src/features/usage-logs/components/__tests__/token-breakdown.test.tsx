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
import { render, screen, within } from '@testing-library/react'
import { expect, test } from 'vitest'

import { LogTokenBreakdown } from '../log-token-breakdown'

test('request detail ring shows exclusive token shares and preserves nested usage in the legend', () => {
  render(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 80, completion_tokens: 20 }}
      other={{
        cache_tokens: 20,
        cache_write_tokens: 10,
        cache_creation_tokens_12h: 3,
        reasoning_tokens: 5,
        image_cache_tokens: 2,
        image: true,
        image_output: 4,
      }}
    />
  )
  const chart = screen.getByRole('img', { name: 'Token distribution' })
  const arcs = [...chart.querySelectorAll('circle > title')].map((title) => [
    title.textContent,
    title.parentElement?.getAttribute('stroke-dasharray'),
    title.parentElement?.getAttribute('stroke-dashoffset'),
  ])
  expect(arcs).toEqual([
    ['Uncached input', '50 100', '0'],
    ['Output Tokens', '20 100', '-50'],
    ['Cache read tokens', '20 100', '-70'],
    ['Cache write tokens', '10 100', '-90'],
  ])
  expect(
    screen.getByRole('img', { name: 'Reasoning tokens · Included in output' })
  ).toBeVisible()
  expect(
    screen.getByText('Cache Write (30m)').closest('dt')?.nextElementSibling
  ).toHaveTextContent('7')
  expect(
    screen.getByText('Image Cache').closest('dt')?.nextElementSibling
  ).toHaveTextContent('2')
  expect(
    screen.getByText('Image Tokens').closest('dt')?.nextElementSibling
  ).toHaveTextContent('4')
})

test('request detail keeps an unclassified remainder neutral and does not invent a total for missing usage', () => {
  const { rerender } = render(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 80, completion_tokens: 20 }}
      other={null}
    />
  )
  const chart = screen.getByRole('img', { name: 'Token distribution' })
  expect(chart.querySelectorAll('circle > title')).toHaveLength(1)
  expect(
    within(chart).getByText('Output Tokens').parentElement
  ).toHaveAttribute('stroke-dasharray', '20 100')
  expect(
    screen.getByText('Unknown').closest('dt')?.nextElementSibling
  ).toHaveTextContent('80')
  rerender(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 0, completion_tokens: 0 }}
      other={null}
    />
  )
  expect(chart.querySelectorAll('circle > title')).toHaveLength(0)
  expect(screen.queryByText('0')).not.toBeInTheDocument()
  rerender(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 0, completion_tokens: 0 }}
      other={{ cache_tokens: 0, cache_write_tokens: 0 }}
    />
  )
  expect(chart.querySelectorAll('circle > title')).toHaveLength(0)
  expect(screen.getAllByText('0').length).toBeGreaterThan(0)
})
