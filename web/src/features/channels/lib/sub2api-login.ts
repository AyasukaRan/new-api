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
import { z } from 'zod'

export const sub2APILoginSchema = z.object({
  email: z.string().trim().email('Invalid email address'),
  password: z
    .string()
    .min(1, 'Password is required')
    .max(4096, 'Invalid parameters'),
})

export const sub2APIImportSchema = z.object({
  access_token: z
    .string()
    .trim()
    .transform((value) => value.replace(/^Bearer[ \t]+/i, ''))
    .pipe(
      z
        .string()
        .min(1, 'Access token is required')
        .max(15000, 'Invalid parameters')
        .regex(/^\S+$/, 'Invalid parameters')
    ),
  refresh_token: z.string().trim().max(15000, 'Invalid parameters'),
})

export const sub2APITotpSchema = z.object({
  totp_code: z
    .string()
    .trim()
    .regex(/^\d{6}$/, 'Enter a 6-digit code'),
})
