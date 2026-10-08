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
    JSON.parse(code)
    return true
  } catch {
    return false
  }
}

export function parseJsonSource(code: string): JsonSyntaxNode | null {
  try {
    JSON.parse(code)
    // Source spans preserve numeric precision, duplicate properties and escapes.
    return jsonParser.parse(code).topNode.firstChild
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
