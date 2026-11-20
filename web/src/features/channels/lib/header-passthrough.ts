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

/**
 * The header override rule key the relay treats as "forward every client
 * request header" (see UpstreamRequestHeaders in relay/common/header_passthrough.go).
 * The quick "Pass Through Request Headers" switch is a shortcut for this rule.
 */
export const HEADER_PASSTHROUGH_ALL_KEY = '*'

export type HeaderPassthroughState = 'enabled' | 'disabled' | 'invalid'

function parseHeaderOverrideObject(
  headerOverride: string | undefined
): Record<string, unknown> | null {
  const trimmed = headerOverride?.trim()
  if (!trimmed) return {}
  try {
    const parsed: unknown = JSON.parse(trimmed)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // Fall through: invalid JSON cannot be edited structurally.
  }
  return null
}

function isPassthroughAllKey(key: string): boolean {
  return key.trim() === HEADER_PASSTHROUGH_ALL_KEY
}

export function getHeaderPassthroughState(
  headerOverride: string | undefined
): HeaderPassthroughState {
  const parsed = parseHeaderOverrideObject(headerOverride)
  if (parsed === null) return 'invalid'
  const wildcard = Object.entries(parsed).find(([key]) =>
    isPassthroughAllKey(key)
  )
  if (wildcard) {
    return String(wildcard[1]).toLowerCase() === 'false'
      ? 'disabled'
      : 'enabled'
  }
  return Object.keys(parsed).some((key) => /^(?:re|regex):/i.test(key.trim()))
    ? 'disabled'
    : 'enabled'
}

export function setHeaderPassthrough(
  headerOverride: string | undefined,
  enabled: boolean
): string {
  const parsed = parseHeaderOverrideObject(headerOverride)
  if (parsed === null) return headerOverride ?? ''
  const rest = Object.fromEntries(
    Object.entries(parsed).filter(([key]) => !isPassthroughAllKey(key))
  )
  return JSON.stringify(
    { [HEADER_PASSTHROUGH_ALL_KEY]: enabled, ...rest },
    null,
    2
  )
}
