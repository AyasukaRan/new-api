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
 * Sub-path deployment prefix, frozen at build time from `VITE_BASE_PATH`
 * (e.g. `/new-api`). Empty means the app is served from the domain root.
 *
 * The Go server registers both the static assets and every API under the root
 * (`static.Serve("/")` in router/web-router.go, `/api` and the relay groups in
 * router/*.go) and does not read `X-Forwarded-Prefix`, so a reverse proxy has
 * to strip the prefix. This constant is the other half of that pair: it puts
 * the prefix back onto every URL the browser emits. Changing the prefix means
 * rebuilding the frontend.
 */
export const BASE_PATH: string = (import.meta.env.VITE_BASE_PATH || '').replace(
  /\/+$/,
  ''
)

/**
 * Public base URL of this deployment as seen by the browser, prefix included.
 * Used for the API base URL shown to users when no server address is configured.
 */
export function currentBaseUrl(): string {
  if (typeof window === 'undefined') return ''
  return `${window.location.origin}${BASE_PATH}`
}

/** Prefixes a root-absolute path for sub-path deployments; a no-op at the root. */
export function withBasePath(path: string): string {
  if (!BASE_PATH || !path.startsWith('/')) return path
  return `${BASE_PATH}${path}`
}
