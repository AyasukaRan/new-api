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
import { javascriptLanguage } from '@codemirror/lang-javascript'

const jsonParser = javascriptLanguage.parser.configure({
  top: 'SingleExpression',
})
export type JsonSyntaxNode = ReturnType<typeof jsonParser.parse>['topNode']

/** Validate JSON without using the parsed values, which can round large numbers. */
export function isStructuredJson(code: string): boolean {
  const trimmed = code.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return false
  try {
    JSON.parse(trimmed)
    return true
  } catch {
    return false
  }
}

export function parseJsonSource(code: string): JsonSyntaxNode | null {
  try {
    // Match detection's whitespace/BOM handling without shifting source spans.
    JSON.parse(code.trim())
    const tree = jsonParser.parse(code)
    const root = tree.topNode.firstChild
    if (
      !root ||
      code.slice(0, root.from).trim() ||
      code.slice(root.to).trim()
    ) {
      return null
    }

    // Valid JSON can still exceed the JavaScript parser's recovery limits or
    // contain unsupported Unicode. A partial/recovered tree must not hide data.
    const cursor = tree.cursor()
    do {
      if (cursor.type.isError) return null
    } while (cursor.next())

    // Source spans preserve numeric precision, duplicate properties and escapes.
    return root
  } catch {
    return null
  }
}

export type JsonChild = {
  label: string
  name: string
  node: JsonSyntaxNode
}

export function getJsonChildren(
  node: JsonSyntaxNode,
  code: string
): JsonChild[] {
  const children: JsonChild[] = []
  if (node.name !== 'ObjectExpression' && node.name !== 'ArrayExpression') {
    return children
  }

  for (let child = node.firstChild; child; child = child.nextSibling) {
    if (node.name === 'ObjectExpression') {
      if (child.name !== 'Property' || !child.firstChild || !child.lastChild) {
        continue
      }
      const label = code.slice(child.firstChild.from, child.firstChild.to)
      children.push({
        label,
        name: JSON.parse(label) as string,
        node: child.lastChild,
      })
    } else if (!['[', ']', ','].includes(child.name)) {
      const label = String(children.length)
      children.push({ label, name: label, node: child })
    }
  }
  return children
}
