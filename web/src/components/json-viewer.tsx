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
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlock, CodeBlockFrame } from '@/components/ai-elements/code-block'
import { CopyButton } from '@/components/copy-button'
import { parseJsonSource } from '@/components/json-viewer/json-source'
import { JsonTree } from '@/components/json-viewer/json-tree'
import { Button } from '@/components/ui/button'

type JsonViewerProps = {
  code: string
  title?: string
}

export function JsonViewer(props: JsonViewerProps) {
  const { t } = useTranslation()
  const [raw, setRaw] = useState(false)
  const root = useMemo(() => parseJsonSource(props.code), [props.code])
  const actions = (
    <>
      {root && (
        <Button
          size='xs'
          type='button'
          variant='ghost'
          onClick={() => setRaw((value) => !value)}
        >
          {raw ? t('JSON tree') : t('Raw JSON')}
        </Button>
      )}
      <CopyButton value={props.code} tooltip={t('Copy JSON')} />
    </>
  )
  if (!root || raw) {
    return (
      <CodeBlock
        code={props.code}
        language='json'
        title={props.title ?? 'JSON'}
        showToolbar
        showLineNumbers
        maxExpandedLines={20}
      >
        {actions}
      </CodeBlock>
    )
  }
  return (
    <CodeBlockFrame
      bodyClassName='overflow-visible p-0'
      endActions={actions}
      showToolbar
      title={props.title ?? 'JSON'}
    >
      <JsonTree key={props.code} code={props.code} root={root} />
    </CodeBlockFrame>
  )
}
