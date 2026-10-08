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
import { isStructuredJson } from '@/components/json-viewer/json-source'

/** Recognize a JSON document or a single JSON fence, preserving its source. */
export function getTraceJsonContent(content: string): string | undefined {
  const trimmed = content.trim()
  if (isStructuredJson(trimmed)) return content

  // Only a whole fence is unwrapped here. Mixed Markdown stays in Response so
  // lists, quotes and surrounding prose retain their original structure.
  const fence = trimmed.match(/^(`{3,}|~{3,})([^\r\n]*)\r?\n/)
  if (!fence) return undefined
  const language = fence[2].trim().toLowerCase()
  if (language !== 'json' && language !== '') return undefined

  const remainder = trimmed.slice(fence[0].length)
  const closing = remainder.match(
    new RegExp(`^ {0,3}${fence[1][0]}{${fence[1].length},}[ \t]*\r?$`, 'm')
  )
  if (
    !closing ||
    closing.index === undefined ||
    closing.index + closing[0].length !== remainder.length
  ) {
    return undefined
  }
  const code = remainder.slice(0, closing.index)
  if (language === 'json' || isStructuredJson(code)) return code
  return undefined
}
