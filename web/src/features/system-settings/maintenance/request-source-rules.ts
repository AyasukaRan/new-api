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
import { z } from 'zod'

const byteLength = (value: string) => new TextEncoder().encode(value).length

function validSourceHeader(value: string): boolean {
  if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(value)) return false
  const compact = value.toLowerCase().replaceAll(/[-_]/g, '')
  return (
    !/authorization|authentication|cookie|apikey|accesskey|password|secret|credential/.test(
      compact
    ) && !compact.endsWith('token')
  )
}

function validSourceName(value: string): boolean {
  if (
    !value.trim() ||
    byteLength(value) > 64 ||
    !/^[\p{L}\p{Nd} ._-]+$/u.test(value)
  ) {
    return false
  }
  return value
    .toLowerCase()
    .split(/ +/)
    .every(
      (word) =>
        !['bearer', 'basic'].includes(word) &&
        ![
          'sk-',
          'sk_',
          'ak-',
          'ak_',
          'ghp_',
          'github_pat_',
          'xoxb-',
          'xoxp-',
          'gsk_',
          'aiza',
          'eyj',
        ].some((prefix) => word.startsWith(prefix))
    )
}

export function requestSourceRulesSchema(t: TFunction) {
  const condition = z
    .object({
      header: z
        .string()
        .refine(
          validSourceHeader,
          t('Enter a non-sensitive HTTP header name.')
        ),
      match: z.enum(['exact', 'prefix', 'contains', 'regex']),
      value: z
        .string()
        .min(1, t('Enter a pattern of up to 512 bytes.'))
        .refine(
          (value) => byteLength(value) <= 512,
          t('Enter a pattern of up to 512 bytes.')
        ),
      case_sensitive: z.boolean(),
    })
    .strict()
  const rule = z
    .object({
      enabled: z.boolean(),
      name: z
        .string()
        .refine(
          validSourceName,
          t('Enter a valid source name of up to 64 bytes.')
        )
        .transform((value) => value.trim()),
      conditions: z
        .array(condition)
        .min(1, t('Use between 1 and 8 conditions per rule.'))
        .max(8, t('Use between 1 and 8 conditions per rule.')),
    })
    .strict()
  return z.object({
    rules: z
      .array(rule)
      .max(100, t('Use at most 100 source rules.'))
      .refine(
        (rules) => byteLength(JSON.stringify(rules)) <= 128 * 1024,
        t('Source rules must not exceed 128 KiB.')
      ),
  })
}

export type RequestSourceRulesValues = z.output<
  ReturnType<typeof requestSourceRulesSchema>
>
export type RequestSourceRule = RequestSourceRulesValues['rules'][number]
export type RequestSourceCondition = RequestSourceRule['conditions'][number]

export function parseRequestSourceRules(
  raw: string,
  t: TFunction
): RequestSourceRule[] | null {
  if (byteLength(raw) > 128 * 1024) return null
  try {
    const parsed = requestSourceRulesSchema(t).safeParse({
      rules: JSON.parse(raw),
    })
    return parsed.success ? parsed.data.rules : null
  } catch {
    return null
  }
}
