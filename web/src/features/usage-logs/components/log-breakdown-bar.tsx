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
export function LogBreakdownBar(props: {
  label: string
  total: number
  segments: ReadonlyArray<{
    key: string
    label: string
    value: number
    color: string
  }>
}) {
  if (!Number.isFinite(props.total) || props.total <= 0) return null
  const segments = props.segments.filter(
    (segment) => Number.isFinite(segment.value) && segment.value > 0
  )
  if (!segments.length) return null
  return (
    <div
      role='img'
      aria-label={props.label}
      className='bg-muted flex h-2.5 overflow-hidden rounded-full'
    >
      {segments.map((segment) => (
        <span
          key={segment.key}
          title={segment.label}
          className={segment.color}
          style={{
            width: `${Math.min(100, (segment.value / props.total) * 100)}%`,
          }}
        />
      ))}
    </div>
  )
}
