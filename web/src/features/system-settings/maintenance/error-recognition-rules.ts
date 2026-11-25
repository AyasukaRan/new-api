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

function errorRuleFields(t: TFunction) {
  return z.object({
    name: z
      .string()
      .regex(
        /^[A-Za-z0-9_-]{1,64}$/,
        t(
          'Use a unique rule ID of up to 64 letters, numbers, underscores or hyphens.'
        )
      ),
    label: z
      .string()
      .refine(
        (value) =>
          !!value.trim() && byteLength(value) <= 128 && !/\p{Cc}/u.test(value),
        t('Enter a short error label of up to 128 bytes.')
      ),
    enabled: z.boolean(),
    match: z.enum(['contains', 'regex', 'exact']),
    pattern: z
      .string()
      .refine(
        (value) => !!value.trim() && byteLength(value) <= 2048,
        t('Enter an error pattern of up to 2048 bytes.')
      ),
  })
}

export function errorRecognitionRulesSchema(t: TFunction) {
  return z.object({
    rules: z
      .array(
        errorRuleFields(t)
          .extend({
            status_codes: z
              .array(z.number().int().min(400).max(599))
              .max(100)
              .optional(),
          })
          .strict()
      )
      .max(100, t('Use at most 100 error rules.'))
      .superRefine((rules, context) => {
        const names = new Set<string>()
        rules.forEach((rule, index) => {
          if (names.has(rule.name)) {
            context.addIssue({
              code: 'custom',
              path: [index, 'name'],
              message: t('Rule IDs must be unique.'),
            })
          }
          names.add(rule.name)
        })
      }),
  })
}

export type ErrorRecognitionRule = z.output<
  ReturnType<typeof errorRecognitionRulesSchema>
>['rules'][number]

export function errorRecognitionFormSchema(t: TFunction) {
  return z.object({
    rules: z
      .array(
        errorRuleFields(t).extend({
          status_codes: z
            .string()
            .refine(
              (value) =>
                !value.trim() ||
                (value.split(',').length <= 100 &&
                  value
                    .split(',')
                    .every((item) => /^\s*[45]\d{2}\s*$/.test(item))),
              t(
                'Enter comma-separated status codes between 400 and 599, or leave empty.'
              )
            ),
        })
      )
      .max(100, t('Use at most 100 error rules.'))
      .superRefine((rules, context) => {
        const names = new Set<string>()
        rules.forEach((rule, index) => {
          if (names.has(rule.name)) {
            context.addIssue({
              code: 'custom',
              path: [index, 'name'],
              message: t('Rule IDs must be unique.'),
            })
          }
          names.add(rule.name)
        })
      }),
  })
}

export type ErrorRecognitionFormValues = z.output<
  ReturnType<typeof errorRecognitionFormSchema>
>

export function serializeErrorRecognitionRules(
  rules: ErrorRecognitionFormValues['rules']
): ErrorRecognitionRule[] {
  return rules.map((rule) => ({
    ...rule,
    status_codes: rule.status_codes.trim()
      ? rule.status_codes.split(',').map((value) => Number(value.trim()))
      : undefined,
  }))
}

export function parseErrorRecognitionRules(
  raw: string,
  t: TFunction
): ErrorRecognitionRule[] | null {
  try {
    const result = errorRecognitionRulesSchema(t).safeParse({
      rules: JSON.parse(raw),
    })
    return result.success ? result.data.rules : null
  } catch {
    return null
  }
}
