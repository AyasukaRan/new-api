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
import { useMemo, type ReactNode } from 'react'

import { Response } from '@/components/ai-elements/response'
import { JsonViewer } from '@/components/json-viewer'
import { isStructuredJson } from '@/components/json-viewer/json-source'

import {
  getTraceJsonContent,
  isJsonFenceLanguage,
  requestTraceMarkdown,
} from '../../lib/request-trace-json'

function renderTraceCodeBlock(code: string, language: string): ReactNode {
  const normalized = language.trim().toLowerCase()
  if (
    isJsonFenceLanguage(normalized) ||
    ((normalized === 'plaintext' || normalized === 'text') &&
      isStructuredJson(code))
  ) {
    return <JsonViewer code={code} />
  }
  return undefined
}

export function RequestTraceContent(props: { content: string }) {
  const json = useMemo(
    () => getTraceJsonContent(props.content),
    [props.content]
  )
  if (json !== undefined) return <JsonViewer code={json} />

  const hasJsonBlock =
    /(`{3,}|~{3,})[ \t]*(?:(?:application\/)?json\b|\r?\n\s*(?:>\s*)*[{[])/i.test(
      props.content
    ) || /^(?:[ \t]*>[ \t]*)*[ \t]*[{[]/m.test(props.content)
  return (
    <Response
      final
      className='whitespace-pre-wrap'
      markdown={requestTraceMarkdown}
      renderCodeBlock={renderTraceCodeBlock}
      maxMarkdownCharacters={hasJsonBlock ? 2_000_000 : undefined}
    >
      {props.content}
    </Response>
  )
}
