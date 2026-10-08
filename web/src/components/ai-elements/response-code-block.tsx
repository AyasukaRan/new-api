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
import { createContext, useContext, type ReactNode } from 'react'

import { CodeBlock, CodeBlockCopyButton } from './code-block'
import type { ResponseCodeBlockRenderer } from './response-types'

const ResponseCodeBlockContext = createContext<
  ResponseCodeBlockRenderer | undefined
>(undefined)

export function ResponseCodeBlockProvider(props: {
  renderCodeBlock?: ResponseCodeBlockRenderer
  children: ReactNode
}) {
  return (
    <ResponseCodeBlockContext.Provider value={props.renderCodeBlock}>
      {props.children}
    </ResponseCodeBlockContext.Provider>
  )
}

export function ResponseCodeBlock(props: { code: string; language: string }) {
  const renderCodeBlock = useContext(ResponseCodeBlockContext)
  const customContent = renderCodeBlock?.(props.code, props.language)
  if (customContent !== undefined) return customContent

  const lineCount = props.code.split('\n').length
  return (
    <CodeBlock
      collapsedLines={14}
      code={props.code}
      defaultCollapsed={lineCount > 14}
      language={props.language}
      maxExpandedLines={44}
      showLineNumbers
      showToolbar
      title={props.language}
    >
      <CodeBlockCopyButton />
    </CodeBlock>
  )
}
