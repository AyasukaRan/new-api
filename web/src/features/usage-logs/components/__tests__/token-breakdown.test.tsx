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
        text_input: 66,
        text_output: 15,
        audio_input_token_count: 10,
        audio_output: 0,
        cached_tokens_details: { text_tokens: 18, image_tokens: 2 },
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
  const table = within(
    screen.getByRole('table', { name: 'Tokens by modality' })
  )
  const cells = (name: RegExp) =>
    within(table.getByRole('row', { name }))
      .getAllByRole('cell')
      .map((cell) => cell.textContent)
  expect(cells(/^Image/)).toEqual(['4', '2', '—'])
  expect(cells(/^Text/)).toEqual(['66', '18', '15'])
  expect(cells(/^Audio/)).toEqual(['10', '—', '0'])
  expect(
    screen.getByText('Non-reasoning output').nextElementSibling
  ).toHaveTextContent('15')
})

test('shows cache hits as a share of all input and only derives output composition from recorded reasoning', () => {
  const { rerender, container } = render(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 3976, completion_tokens: 333 }}
      other={{ cache_ratio: 0.1, cache_tokens: 256 }}
    />
  )
  expect(
    screen.getByText('Total input tokens').nextElementSibling
  ).toHaveTextContent('3,976')
  expect(
    screen.getByText('Input cache hit rate').nextElementSibling
  ).toHaveTextContent('6.4%')
  expect(screen.getByText('5.9%')).toBeVisible()
  for (const value of [
    screen.getByTitle('4,309'),
    screen.getByText('3,976'),
    screen.getByText('6.4%'),
  ]) {
    expect(value).toHaveClass('tabular-nums')
    expect(container.contains(value.closest('.font-mono, .font-sans'))).toBe(
      false
    )
  }
  expect(screen.queryByText('Non-reasoning output')).not.toBeInTheDocument()
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  rerender(
    <LogTokenBreakdown
      variant='detail'
      log={{ prompt_tokens: 3720, completion_tokens: 333 }}
      other={{
        usage_semantic: 'anthropic',
        cache_tokens: 256,
        reasoning_tokens: 0,
      }}
    />
  )
  expect(
    screen.getByText('Total input tokens').nextElementSibling
  ).toHaveTextContent('Not recorded')
  expect(
    screen.getByText('Input cache hit rate').nextElementSibling
  ).toHaveTextContent('Not recorded')
  expect(
    screen.getByText('Non-reasoning output').nextElementSibling
  ).toHaveTextContent('333')
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
