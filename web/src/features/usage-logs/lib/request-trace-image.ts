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

export type TraceImagePart = { type: 'image'; src?: string }

// Use the same maximum as the gateway's trace body limit. Only decode the
// signature here; the browser image decoder handles the actual picture.
const maxEncodedLength = 32 * 1024 * 1024

function imageMime(format: unknown): string {
  if (typeof format !== 'string' || !format.trim()) return ''
  const subtype = format
    .trim()
    .toLowerCase()
    .replace(/^image\//, '')
  return `image/${subtype === 'jpg' ? 'jpeg' : subtype}`
}

export function normalizeTraceImage(
  data: unknown,
  mediaType?: unknown
): TraceImagePart {
  const unavailable: TraceImagePart = { type: 'image' }
  if (
    typeof data !== 'string' ||
    !data.length ||
    data.length > maxEncodedLength
  ) {
    return unavailable
  }
  let mime = imageMime(mediaType)
  let payload = data.trim()
  if (/^data:/i.test(payload)) {
    const header = /^data:(image\/[\w.+-]+);base64,/i.exec(payload)
    if (!header || (mime && mime !== imageMime(header[1]))) {
      return unavailable
    }
    mime = imageMime(header[1])
    payload = payload.slice(header[0].length)
  }
  if (
    mime &&
    !['image/jpeg', 'image/png', 'image/webp', 'image/gif'].includes(mime)
  ) {
    return unavailable
  }
  if (/[\t\n\r ]/.test(payload)) payload = payload.replaceAll(/[\t\n\r ]+/g, '')
  if (payload.length < 4 || /[^A-Za-z0-9+/=]/.test(payload)) return unavailable
  const padding = payload.indexOf('=')
  if (padding !== -1) {
    const suffix = payload.slice(padding, padding + 3)
    if (
      payload.length % 4 ||
      (suffix !== '=' && suffix !== '==') ||
      padding + suffix.length !== payload.length
    ) {
      return unavailable
    }
  } else if (payload.length % 4 === 1) {
    return unavailable
  }
  let header: string
  try {
    header = atob(payload.slice(0, 16))
  } catch {
    return unavailable
  }
  let detected: string
  if (header.startsWith('\x89PNG\r\n\x1a\n')) detected = 'image/png'
  else if (header.startsWith('\xff\xd8\xff')) detected = 'image/jpeg'
  else if (header.startsWith('GIF87a') || header.startsWith('GIF89a')) {
    detected = 'image/gif'
  } else if (header.startsWith('RIFF') && header.slice(8, 12) === 'WEBP') {
    detected = 'image/webp'
  } else return unavailable
  if (mime && mime !== detected) return unavailable
  return { type: 'image', src: `data:${detected};base64,${payload}` }
}
