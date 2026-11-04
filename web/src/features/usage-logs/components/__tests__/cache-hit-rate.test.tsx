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
import { render, screen, act } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, expect, test } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import type { LogOtherData } from '../../types'
import { CacheHitRate } from '../cache-hit-rate'

afterEach(async () => {
  await i18next.changeLanguage('en')
})

test.each<{
  name: string
  prompt: number
  other: LogOtherData
  expected: string
}>([
  {
    name: 'DeepSeek input includes cache',
    prompt: 70937,
    other: { cache_tokens: 70144 },
    expected: '98.9%',
  },
  {
    name: 'recorded total takes precedence',
    prompt: 62,
    other: { cache_tokens: 3544, input_tokens_total: 4192, claude: true },
    expected: '84.5%',
  },
  {
    name: 'Claude includes writes without counting 12-hour subsets twice',
    prompt: 100,
    other: {
      cache_tokens: 600,
      usage_semantic: 'anthropic',
      cache_creation_tokens: 300,
      cache_creation_tokens_5m: 100,
      cache_creation_tokens_1h: 100,
      cache_creation_tokens_12h: 100,
    },
    expected: '60%',
  },
  {
    name: 'normalized writes take precedence',
    prompt: 100,
    other: {
      cache_tokens: 600,
      claude: true,
      cache_write_tokens: 300,
      cache_creation_tokens: 200,
    },
    expected: '60%',
  },
  {
    name: 'old split-cache input is separate',
    prompt: 62,
    other: { cache_tokens: 3544, cache_creation_tokens_5m: 586 },
    expected: '84.5%',
  },
  {
    name: 'explicit OpenAI semantics include cache',
    prompt: 1000,
    other: {
      cache_tokens: 600,
      usage_semantic: 'openai',
      claude: true,
      cache_creation_tokens_5m: 100,
    },
    expected: '60%',
  },
  {
    name: 'pure-cache Claude input',
    prompt: 0,
    other: { cache_tokens: 1000, claude: true },
    expected: '100%',
  },
  {
    name: 'zero hits',
    prompt: 1000,
    other: { cache_tokens: 0 },
    expected: '0%',
  },
])('shows $expected for $name', ({ prompt, other, expected }) => {
  render(<CacheHitRate promptTokens={prompt} other={other} />)
  expect(screen.getByText(expected)).toBeVisible()
  const bar = screen.getByRole('progressbar', { name: 'Cache hit rate' })
  expect(bar).toHaveAttribute('aria-valuetext', expected)
  expect(Number(bar.getAttribute('aria-valuenow'))).toBeGreaterThanOrEqual(0)
  expect(Number(bar.getAttribute('aria-valuenow'))).toBeLessThanOrEqual(100)
})

test.each<{ prompt: number; other: LogOtherData | null }>([
  { prompt: 100, other: null },
  { prompt: 100, other: {} },
  { prompt: 0, other: { cache_tokens: 0 } },
  { prompt: 100, other: { cache_tokens: -1 } },
  { prompt: 100, other: { cache_tokens: Infinity } },
  { prompt: 100, other: { cache_tokens: 101 } },
])(
  'does not invent a rate for incomplete or invalid usage %j',
  ({ prompt, other }) => {
    render(<CacheHitRate promptTokens={prompt} other={other} />)
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  }
)

test('updates the cache label when the interface language changes', async () => {
  i18next.addResourceBundle('en', 'translation', en.translation)
  i18next.addResourceBundle('zhCN', 'translation', zh.translation)
  render(<CacheHitRate promptTokens={1000} other={{ cache_tokens: 250 }} />)
  expect(
    screen.getByRole('progressbar', { name: 'Cache hit rate' })
  ).toBeVisible()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(screen.getByRole('progressbar', { name: '缓存命中率' })).toBeVisible()
  expect(screen.getByText('25%')).toBeVisible()
})

test.each([
  [0, 'text-orange-700', 'dark:text-orange-400'],
  [499, 'text-orange-700', 'dark:text-orange-400'],
  [500, 'text-amber-700', 'dark:text-amber-400'],
  [799, 'text-amber-700', 'dark:text-amber-400'],
  [800, 'text-emerald-700', 'dark:text-emerald-400'],
])(
  'uses the matching light and dark rate colors for %i cached input tokens',
  (cached, light, dark) => {
    render(
      <CacheHitRate promptTokens={1000} other={{ cache_tokens: cached }} />
    )
    expect(
      screen.getByRole('progressbar', { name: 'Cache hit rate' })
    ).toHaveClass(light, dark)
  }
)
