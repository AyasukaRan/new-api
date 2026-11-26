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
import {
  createDecipheriv,
  generateKeyPairSync,
  privateDecrypt,
  webcrypto,
} from 'node:crypto'

import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  clearPasswordEncryptionCache,
  encryptChannelCredential,
  encryptPassword,
} from './password-encryption'

const { publicKey, privateKey } = generateKeyPairSync('rsa', {
  modulusLength: 2048,
  publicKeyEncoding: { type: 'spki', format: 'pem' },
  privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
})
const credential = `synthetic-token-${'x'.repeat(700)}秘密`
const channelKeyPath = '/api/sub2api/accounts/encryption-key'
const loginKeyPath = '/api/user/login/encryption-key'
const failureMessage = 'Credential encryption failed. Please try again.'

beforeEach(() => {
  clearPasswordEncryptionCache()
})

afterEach(() => {
  clearPasswordEncryptionCache()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

test.each(['HTTP', 'HTTPS'] as const)(
  '%s channel credentials use the admin key and decrypt to the complete long token',
  async (protocol) => {
    vi.stubGlobal(
      'crypto',
      protocol === 'HTTPS'
        ? webcrypto
        : {
            getRandomValues: webcrypto.getRandomValues.bind(webcrypto),
          }
    )
    const webEncrypt = vi.spyOn(webcrypto.subtle, 'encrypt')
    const get = vi.spyOn(api, 'get').mockImplementation(async (path) => {
      if (path === loginKeyPath) {
        return { data: { success: true, data: { enabled: false } } }
      }
      if (path === channelKeyPath) {
        return {
          data: {
            success: true,
            data: { kid: 'channel-key', public_key: publicKey },
          },
        }
      }
      throw new Error('Unexpected encryption endpoint')
    })

    const result = await encryptChannelCredential(credential)

    expect(get).toHaveBeenCalledExactlyOnceWith(channelKeyPath)
    expect(result.encryption_key_id).toBe('channel-key')
    const parts = result.password_encrypted.split('.')
    expect(parts).toHaveLength(4)
    expect(parts[0]).toBe('v2')
    const secret = privateDecrypt(
      {
        key: privateKey,
        oaepHash: 'sha256',
        oaepLabel: Buffer.from('password-v2'),
      },
      Buffer.from(parts[1], 'base64')
    )
    const sealed = Buffer.from(parts[3], 'base64')
    const decipher = createDecipheriv(
      'aes-256-gcm',
      secret,
      Buffer.from(parts[2], 'base64')
    )
    decipher.setAAD(Buffer.from('password-v2:channel-key'))
    decipher.setAuthTag(sealed.subarray(-16))
    expect(
      Buffer.concat([
        decipher.update(sealed.subarray(0, -16)),
        decipher.final(),
      ]).toString('utf8')
    ).toBe(credential)
    if (protocol === 'HTTPS') {
      expect(webEncrypt).toHaveBeenCalledTimes(2)
    } else {
      expect(webEncrypt).not.toHaveBeenCalled()
    }
  }
)

test.each(['unavailable key', 'invalid key', 'network failure'] as const)(
  'channel encryption uses a safe fixed error for %s',
  async (failure) => {
    vi.stubGlobal('crypto', {
      getRandomValues: webcrypto.getRandomValues.bind(webcrypto),
    })
    vi.spyOn(api, 'get').mockImplementation(async () => {
      if (failure === 'network failure') {
        throw new Error(credential)
      }
      if (failure === 'invalid key') {
        return {
          data: {
            success: true,
            data: { kid: 'channel-key', public_key: 'invalid PEM' },
          },
        }
      }
      return { data: { success: true, data: { enabled: false } } }
    })

    const result = await encryptChannelCredential(credential).catch(
      (error: unknown) => error
    )

    expect(result).toBeInstanceOf(Error)
    expect((result as Error).message).toBe(failureMessage)
    expect(String(result)).not.toContain(credential)
    expect((result as Error).stack).not.toContain(credential)
  }
)

test('ordinary password encryption retains its separate login endpoint', async () => {
  vi.stubGlobal('crypto', webcrypto)
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { kid: 'login-key', public_key: publicKey },
    },
  })

  const result = await encryptPassword('synthetic-password')

  expect(get).toHaveBeenCalledExactlyOnceWith(loginKeyPath)
  expect(
    privateDecrypt(
      { key: privateKey, oaepHash: 'sha256' },
      Buffer.from(result.password_encrypted, 'base64')
    ).toString('utf8')
  ).toBe('synthetic-password')
})
