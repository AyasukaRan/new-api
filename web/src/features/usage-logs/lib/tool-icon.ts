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
import {
  Code,
  File,
  FilePenLine,
  FileSearch,
  FolderSearch,
  Globe,
  Image,
  Link,
  Mic,
  Network,
  Terminal,
  Users,
  Wrench,
} from 'lucide-react'

export function getLogToolIcon(name: string) {
  const tool = name.toLowerCase().split('.').pop() || name
  if (/web_?search|search_web|google_search/.test(tool)) return Globe
  if (/web_?fetch|open_url|browse|fetch/.test(tool)) return Link
  if (/file_?search|grep|glob|find/.test(tool)) return FileSearch
  if (/image|draw/.test(tool)) return Image
  if (/audio|speech|transcri/.test(tool)) return Mic
  if (/bash|shell|exec|terminal|pwsh|stdin/.test(tool)) return Terminal
  if (/patch|edit|write/.test(tool)) return FilePenLine
  if (/read|document|file/.test(tool)) return File
  if (/list|directory/.test(tool)) return FolderSearch
  if (/agent/.test(tool)) return Users
  if (/code|python|javascript/.test(tool)) return Code
  if (/network/.test(tool)) return Network
  return Wrench
}
