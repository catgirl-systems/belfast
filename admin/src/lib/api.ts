// The back-office API (internal/admin): JSON, a session cookie.

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
  }
}

let onUnauthorized: () => void = () => {}

export function whenLoggedOut(fn: () => void) {
  onUnauthorized = fn
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch('/api' + path, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    if (response.status === 401 && path !== '/login') onUnauthorized()
    throw new ApiError(response.status, data.error ?? response.statusText)
  }
  return data as T
}

export interface CommanderRow {
  id: number
  account_id: number
  name: string
  level: number
  registered_at: number
  last_online: number
  online: boolean
  ip?: string
}

export interface CommanderSummary extends CommanderRow {
  gold: number
  oil: number
  gems: number
  counts: { items: number; ships: number; meowfficers: number; friends: number; unread_mail: number }
}

export interface Amount {
  id: number
  name: string
  rarity?: number
  // the bundle of its picture, for Icon
  icon?: string
  count: number
}

export interface Ship {
  id: number
  template_id: number
  name: string
  rarity: number
  level: number
  intimacy: number
  locked: boolean
  married: boolean
}

export interface Fleet {
  id: number
  name: string
  ships: number[]
}

export interface Drop {
  type: number
  id: number
  count: number
  name?: string
}

export interface Mail {
  id: number
  date: number
  title: string
  sender: string
  content: string
  attachments: Drop[]
  read: boolean
  claimed: boolean
  important: boolean
  archived: boolean
}

export interface CommanderDetail extends CommanderRow {
  exp: number
  ship_bag_max: number
  equip_bag_max: number
  manifesto: string
  resources: Amount[]
  items: Amount[]
  equipments: Amount[]
  ships: Ship[]
  fleets: Fleet[]
  mails: Mail[]
}

export interface CatalogEntry {
  id: number
  name: string
  rarity: number
  icon?: string
}

export interface Notice {
  id: number
  kind: 'board' | 'gateway'
  version: number
  button: string
  title: string
  image: string
  time: string
  content: string
  tag: number
  icon: number
  priority: number
  need_level: number
}

// DROP_TYPE_* the back-office can grant and name.
export const dropTypes: { type: number; label: string }[] = [
  { type: 1, label: 'Resource' },
  { type: 2, label: 'Item' },
  { type: 3, label: 'Gear' },
  { type: 4, label: 'Ship' },
  { type: 5, label: 'Furniture' },
  { type: 7, label: 'Skin' },
  { type: 8, label: 'Virtual item' },
  { type: 41, label: 'Island item' },
  { type: 46, label: 'Island outfit' },
]
