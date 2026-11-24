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
import type { Table } from '@tanstack/react-table'
import { Columns3 } from 'lucide-react'
import { Fragment } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

import styles from './view-options.module.css'

type DataTableViewOptionsProps<TData> = {
  table: Table<TData>
  columnSwitches?: boolean
}

export function DataTableViewOptions<TData>({
  table,
  columnSwitches = false,
}: DataTableViewOptionsProps<TData>) {
  const { t } = useTranslation()
  const label = columnSwitches ? t('Column settings') : t('View')

  const configurableColumns = table
    .getAllColumns()
    .filter(
      (column) =>
        (column.accessorFn && column.getCanHide()) ||
        column.columnDef.meta?.displayFields?.length
    )

  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger
        render={
          <Button variant='outline' className='shrink-0' aria-label={label} />
        }
      >
        {columnSwitches && <Columns3 aria-hidden className='size-3.5' />}
        {label}
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align='end'
        className={cn(
          'max-h-[65vh] w-52 overflow-y-auto',
          columnSwitches && styles.switches
        )}
      >
        <DropdownMenuGroup>
          <DropdownMenuLabel>{t('Toggle columns')}</DropdownMenuLabel>
          {configurableColumns.map((column) => {
            const hideable = Boolean(column.accessorFn && column.getCanHide())
            return (
              <Fragment key={column.id}>
                {hideable && (
                  <DropdownMenuCheckboxItem
                    className={columnSwitches ? undefined : 'capitalize'}
                    closeOnClick={columnSwitches ? false : undefined}
                    checked={column.getIsVisible()}
                    onCheckedChange={(value) =>
                      column.toggleVisibility(!!value)
                    }
                  >
                    {typeof column.columnDef.header === 'string'
                      ? column.columnDef.header
                      : (column.columnDef.meta?.label ?? column.id)}
                  </DropdownMenuCheckboxItem>
                )}
                {column.getIsVisible() &&
                  column.columnDef.meta?.displayFields?.map(
                    (field: { id: string; label: string }) => (
                      <DropdownMenuCheckboxItem
                        key={field.id}
                        className={cn(
                          hideable &&
                            (columnSwitches ? styles.field : 'pl-10 text-xs')
                        )}
                        closeOnClick={columnSwitches ? false : undefined}
                        checked={
                          table.getState().columnVisibility[field.id] !== false
                        }
                        onCheckedChange={(checked) =>
                          table.setColumnVisibility((old) => ({
                            ...old,
                            [field.id]: !!checked,
                          }))
                        }
                      >
                        {field.label}
                      </DropdownMenuCheckboxItem>
                    )
                  )}
              </Fragment>
            )
          })}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
