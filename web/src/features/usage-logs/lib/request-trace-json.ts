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
import { getMarkdown, type MarkdownIt } from 'stream-markdown-parser'

import { isStructuredJson } from '@/components/json-viewer/json-source'

type TraceJsonBlockState = {
  src: string
  bMarks: number[]
  eMarks: number[]
  tShift: number[]
  sCount: number[]
  blkIndent: number
  line: number
  getLines: (
    begin: number,
    end: number,
    indent: number,
    keepLastLF: boolean
  ) => string
  push: (
    type: string,
    tag: string,
    nesting: number
  ) => ReturnType<MarkdownIt['parse']>[number]
}

const rejectedJsonBlocks = new WeakMap<TraceJsonBlockState, number>()
const MAX_JSON_BLOCK_CHARACTERS = 2_000_000

/** Keep invalid or unfinished outer JSON from exposing nested values as documents. */
function traceJsonBlock(
  state: TraceJsonBlockState,
  startLine: number,
  endLine: number,
  silent: boolean
): boolean {
  if (startLine < (rejectedJsonBlocks.get(state) ?? 0)) return false
  const start = state.bMarks[startLine] + state.tShift[startLine]
  if (state.src[start] !== '{' && state.src[start] !== '[') return false

  const stack: string[] = []
  const lines: string[] = []
  let quoted = false
  let escaped = false
  let size = 0

  for (let line = startLine; line < endLine; line++) {
    if (
      line > startLine &&
      state.bMarks[line] + state.tShift[line] < state.eMarks[line] &&
      state.sCount[line] < state.blkIndent
    ) {
      rejectedJsonBlocks.set(state, line)
      return false
    }
    // Markdown's source map removes quote/list prefixes without changing JSON
    // whitespace or escapes inside the current container.
    const text = state.getLines(line, line + 1, state.blkIndent, false)
    size += text.length + 1
    if (size > MAX_JSON_BLOCK_CHARACTERS) {
      rejectedJsonBlocks.set(state, endLine)
      return false
    }
    lines.push(text)

    for (let index = 0; index < text.length; index++) {
      const character = text[index]
      if (quoted) {
        if (escaped) escaped = false
        else if (character === '\\') escaped = true
        else if (character === '"') quoted = false
        continue
      }
      if (character === '"') {
        quoted = true
        continue
      }
      if (character === '{' || character === '[') {
        stack.push(character)
        continue
      }
      if (character !== '}' && character !== ']') continue
      if (stack.pop() !== (character === '}' ? '{' : '[')) {
        rejectedJsonBlocks.set(state, endLine)
        return false
      }
      if (stack.length > 0) continue
      if (text.slice(index + 1).trim()) {
        rejectedJsonBlocks.set(state, line + 1)
        return false
      }

      const code = lines.join('\n')
      if (!isStructuredJson(code)) {
        rejectedJsonBlocks.set(state, line + 1)
        return false
      }
      if (silent) return true

      const token = state.push('fence', 'code', 0)
      token.info = 'json'
      token.content = code
      token.map = [startLine, line + 1]
      token.meta = { closed: true }
      state.line = line + 1
      return true
    }

    // A literal newline cannot continue a JSON string.
    if (quoted) {
      rejectedJsonBlocks.set(state, endLine)
      return false
    }
  }
  rejectedJsonBlocks.set(state, endLine)
  return false
}

function configureTraceMarkdown(markdown: MarkdownIt): void {
  markdown.block.ruler.before(
    'paragraph',
    'request_trace_json',
    traceJsonBlock,
    {
      alt: ['paragraph'],
    }
  )
}

// The parser instance is local to request traces; ordinary chat Markdown keeps
// its existing behavior. A block rule also preserves surrounding references,
// lists and quotes without inspecting the contents of other code fences.
export const requestTraceMarkdown = getMarkdown('new-api-request-trace', {
  apply: [configureTraceMarkdown],
})

export function isJsonFenceLanguage(info: string): boolean {
  const language = info.trim().split(/\s+/, 1)[0].toLowerCase()
  return language === 'json' || language === 'application/json'
}

/** Recognize a JSON document or a single JSON fence, preserving its source. */
export function getTraceJsonContent(content: string): string | undefined {
  const trimmed = content.trim()
  if (isStructuredJson(trimmed)) return content

  // Only a whole fence is unwrapped here. Mixed Markdown stays in Response so
  // lists, quotes and surrounding prose retain their original structure.
  const fence = trimmed.match(/^(`{3,}|~{3,})([^\r\n]*)\r?\n/)
  if (!fence) return undefined
  const isJsonFence = isJsonFenceLanguage(fence[2])
  if (!isJsonFence && fence[2].trim() !== '') return undefined

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
  if (isJsonFence || isStructuredJson(code)) return code
  return undefined
}
