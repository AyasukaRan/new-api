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
// Isolate inline and same-origin previews from the platform. External previews
// retain their own origin, while an explicit caller policy can only restrict it.
export function iframeSandbox(
  src: string | undefined,
  parentOrigin: string,
  requestedPolicy?: string
): string {
  const permissions = [
    'allow-scripts',
    'allow-forms',
    'allow-popups',
    'allow-presentation',
    'allow-downloads',
  ]
  if (src) {
    try {
      const target = new URL(src, parentOrigin)
      if (
        (target.protocol === 'https:' || target.protocol === 'http:') &&
        target.origin !== parentOrigin
      ) {
        permissions.push('allow-same-origin')
      }
    } catch {
      // Invalid and opaque URLs keep the restrictive sandbox.
    }
  }
  if (requestedPolicy === undefined) return permissions.join(' ')
  const requested = new Set(requestedPolicy.toLowerCase().split(/\s+/))
  return permissions.filter((permission) => requested.has(permission)).join(' ')
}
