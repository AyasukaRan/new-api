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
import type { DashboardPanel } from './types'

/** A moved card keeps its requested slot; colliding cards move down in stable order. */
export function placeDashboardPanel(
  panels: DashboardPanel[],
  id: string,
  layout: DashboardPanel['layout']
): DashboardPanel[] {
  const target = panels.find((panel) => panel.id === id)
  if (!target) return panels
  const w = Math.max(3, Math.min(12, Math.round(layout.w)))
  const next = {
    ...target,
    layout: {
      x: Math.max(0, Math.min(12 - w, Math.round(layout.x))),
      y: Math.max(0, Math.min(4096, Math.round(layout.y))),
      w,
      h: Math.max(3, Math.min(12, Math.round(layout.h))),
    },
  }
  const placed = [next]
  const remaining = panels
    .filter((panel) => panel.id !== id)
    .sort((a, b) => a.layout.y - b.layout.y || a.layout.x - b.layout.x)
  for (const panel of remaining) {
    const candidate = { ...panel, layout: { ...panel.layout } }
    let collision: DashboardPanel | undefined
    do {
      collision = placed.find(
        (item) =>
          candidate.layout.x < item.layout.x + item.layout.w &&
          candidate.layout.x + candidate.layout.w > item.layout.x &&
          candidate.layout.y < item.layout.y + item.layout.h &&
          candidate.layout.y + candidate.layout.h > item.layout.y
      )
      if (collision) {
        candidate.layout.y = collision.layout.y + collision.layout.h
      }
    } while (collision)
    if (candidate.layout.y > 4096) return panels
    placed.push(candidate)
  }
  return panels.map(
    (panel) => placed.find((item) => item.id === panel.id) ?? panel
  )
}
