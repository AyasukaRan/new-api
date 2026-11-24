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
import { useRef, useState, type ReactElement, type ReactNode } from 'react'

import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

export function LogMetricPopover(props: {
  label: string
  trigger: ReactElement
  children: ReactNode
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const anchor = useRef<HTMLButtonElement>(null)
  const popup = useRef<HTMLDivElement>(null)

  return (
    <Popover
      open={open}
      onOpenChange={(next, details) => {
        if (
          !next &&
          details.reason === 'escape-key' &&
          popup.current?.contains(document.activeElement)
        ) {
          anchor.current?.focus({ preventScroll: true })
        }
        setOpen(next)
      }}
    >
      <PopoverTrigger
        ref={anchor}
        render={props.trigger}
        openOnHover
        delay={200}
        closeDelay={120}
        onFocus={(event) => {
          if (
            event.currentTarget.matches(':focus-visible') &&
            !popup.current?.contains(event.relatedTarget)
          ) {
            setOpen(true)
          }
        }}
      />
      <PopoverContent
        ref={popup}
        align='end'
        sideOffset={6}
        collisionPadding={8}
        initialFocus={false}
        finalFocus={false}
        className={cn(
          'max-h-[min(70dvh,var(--available-height))] w-72 max-w-[min(calc(100vw-1rem),var(--available-width))] gap-2 overflow-auto overscroll-contain p-3 text-xs leading-4',
          props.className
        )}
      >
        <PopoverTitle className='text-xs font-semibold'>
          {props.label}
        </PopoverTitle>
        {props.children}
      </PopoverContent>
    </Popover>
  )
}
