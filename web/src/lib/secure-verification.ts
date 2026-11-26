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
import axios from 'axios'

import {
  getServerErrorMessageKey,
  getServerMessage,
  safeServerErrorMessage,
} from './server-error-message'

export class AuthOperationError extends Error {
  readonly [safeServerErrorMessage] = true
  readonly retryAfterSeconds?: number
  constructor(
    message: string,
    readonly code?: string,
    options?: ErrorOptions & { retryAfterSeconds?: number }
  ) {
    super(message, options)
    this.name = 'AuthOperationError'
    this.retryAfterSeconds = options?.retryAfterSeconds
  }

  static from(
    error: unknown,
    fallback = 'Verification failed. Please try again.'
  ): AuthOperationError {
    if (error instanceof AuthOperationError) return error
    if (axios.isAxiosError<{ message?: string; code?: string }>(error)) {
      if (error.response?.status === 429) {
        const headers = error.response.headers
        const raw = headers?.['retry-after'] ?? headers?.['Retry-After']
        const value = typeof raw === 'string' ? raw.trim() : ''
        let delay = 30
        if (/^\d+$/.test(value) && Number.isSafeInteger(Number(value))) {
          delay = Number(value)
        } else if (/^[A-Za-z]{3,9}[ ,]/.test(value) && value.length <= 128) {
          const date = Date.parse(value)
          if (Number.isFinite(date)) {
            delay = Math.ceil((date - Date.now()) / 1000)
          }
        }
        const knownMessage = getServerErrorMessageKey(error)
        return new AuthOperationError(
          knownMessage || 'Too many requests',
          knownMessage ? error.response.data?.code : 'AUTH_RATE_LIMITED',
          {
            cause: error,
            retryAfterSeconds: Math.min(86400, Math.max(1, delay)),
          }
        )
      }
      return new AuthOperationError(
        getServerErrorMessageKey(error) ||
          (error.response && error.response.status >= 500
            ? 'Please try again later.'
            : undefined) ||
          getServerMessage(error.response?.data) ||
          error.message ||
          fallback,
        error.response?.data?.code,
        { cause: error }
      )
    }
    return new AuthOperationError(
      error instanceof Error ? error.message : fallback,
      undefined,
      { cause: error }
    )
  }
}

export const authRequestOptions = {
  skipBusinessError: true,
  skipErrorHandler: true,
}

export async function authResult<T>(
  request: Promise<{
    data: { success: boolean; message?: string; code?: string; data?: T }
  }>,
  fallback = 'Verification failed. Please try again.'
): Promise<T> {
  try {
    const { data: response } = await request
    if (!response.success || response.data === undefined) {
      throw new AuthOperationError(
        getServerErrorMessageKey(response) ||
          getServerMessage(response) ||
          fallback,
        response.code
      )
    }
    return response.data
  } catch (error) {
    throw AuthOperationError.from(error, fallback)
  }
}
