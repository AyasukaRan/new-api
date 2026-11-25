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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'

import { getUsers, searchUsers } from '@/features/users/api'
import {
  formatAdminUserLabel,
  getAdminNickname,
} from '@/features/users/lib/admin-nickname'
import { useDebounce } from '@/hooks'
import { useModelAccessScope } from '@/hooks/use-model-access-scope'
import { requireServerSuccess } from '@/lib/server-error-message'

export function useLogUserOptions() {
  const scope = useModelAccessScope()
  const [search, setSearch] = useState<string | null>(null)
  const term = search?.trim() ?? ''
  const debounced = useDebounce(term, 250)
  const keyword = term ? debounced : ''
  const query = useQuery({
    queryKey: ['log-username-options', ...scope, keyword],
    enabled: search !== null && keyword === term,
    staleTime: 60_000,
    queryFn: async () => {
      const page = { p: 1, page_size: 50 }
      const response = keyword
        ? await searchUsers({ ...page, keyword })
        : await getUsers(page)
      const users = requireServerSuccess(response).data?.items ?? []
      return users.map(({ id, username, remark }) => ({
        id,
        username,
        label: formatAdminUserLabel(username, getAdminNickname(remark)),
      }))
    },
  })
  return {
    users: query.data ?? [],
    loading: query.isFetching || keyword !== term,
    setSearch,
    keyword: term,
  }
}
