// The commander screens' API (internal/admin/area_players.go): profile,
// items, gear, wardrobe and mail.
import type { Amount, CatalogEntry, CommanderRow } from './api'
import { keys } from './query'

export interface PlayerProfile extends CommanderRow {
  exp: number
  // exp to the next level, 0 at the last one
  level_exp: number
  max_level: number
  manifesto: string
  secretary: { ship_id: number; name: string; level: number; rarity: number; icon?: string } | null
  guild: { id: number; name: string; duty: number } | null
  counts: {
    ships: number
    ship_bag_max: number
    equipments: number
    equip_bag_max: number
    skins: number
    meowfficers: number
    friends: number
    spweapons: number
    furniture: number
    oaths: number
  }
  secondary_password: boolean
}

export interface PlayerItem extends Amount {
  // item_data_statistics max_num, absent for none
  max?: number
}

export interface GearRow extends Amount {
  enhance: number
}

export interface SpWeaponLevel {
  template_id: number
  // its place in the chain
  level: number
  max_1: number
  max_2: number
  // the enhancement its row gives (back to +0 at a new tier), its name
  plus: number
  name: string
  rarity: number
}

export interface SpWeapon {
  id: number
  template_id: number
  name: string
  rarity: number
  icon?: string
  unique: boolean
  level: number
  levels: SpWeaponLevel[]
  attribute_1: string
  attribute_2: string
  value_1: number
  value_2: number
  attr_1: number
  attr_2: number
  pt: number
  ship_id?: number
  ship_name?: string
}

export interface PlayerGear {
  equipments: GearRow[]
  equip_bag_max: number
  spweapons: SpWeapon[]
  equip_skins: Amount[]
}

// a gear to grant with its ids from +0 up (/catalog/more?type=3)
export interface GearCatalogEntry extends CatalogEntry {
  levels: number[]
}

export interface PlayerSkin {
  id: number
  name: string
  ship: string
  icon?: string
  // 0 for good
  end_time: number
}

export interface PlayerFrame {
  type: number
  id: number
  name: string
  // icon frames only
  icon?: string
  in_use: boolean
}

export interface PlayerWardrobe {
  skins: PlayerSkin[]
  furniture: Amount[]
  frames: PlayerFrame[]
  points: Amount[]
}

export interface Attachment {
  type: number
  id: number
  count: number
  name?: string
  rarity?: number
  icon?: string
}

export interface PlayerMail {
  id: number
  date: number
  // compensations only
  expires?: number
  title: string
  sender: string
  content: string
  attachments: Attachment[]
  read: boolean
  claimed: boolean
  important: boolean
}

export interface PlayerMails {
  inbox: PlayerMail[]
  storage: PlayerMail[]
  compensations: PlayerMail[]
  inbox_max: number
  storage_max: number
}

// the commander screens' queries: under keys.commander(id), so any write to
// the commander refetches them (and the header)
export const playerKeys = {
  profile: (id: number) => keys.screen(id, 'profile'),
  items: (id: number) => keys.screen(id, 'items'),
  gear: (id: number) => keys.screen(id, 'gear'),
  wardrobe: (id: number) => keys.screen(id, 'wardrobe'),
  mails: (id: number) => keys.screen(id, 'mails'),
  // /catalog/more: gear at +0 with its levels, gear skins, frames, augments
  more: (type: number) => keys.catalog('more', type),
}

// the art of a skin (or ship) as the shipyard shows it, from its squareicon
export const cardArt = (icon?: string) => icon?.replace(/^squareicon\//, 'shipyardicon/')

// DROP_TYPE_* the commander screens grant
export const drop = {
  resource: 1,
  item: 2,
  equip: 3,
  ship: 4,
  furniture: 5,
  skin: 7,
  vitem: 8,
  equipSkin: 9,
  iconFrame: 14,
  chatFrame: 15,
  spweapon: 21,
  skinTimed: 23,
  islandItem: 41,
  islandDress: 46,
} as const

// AttributeType names as the client abbreviates them
export const statLabels: Record<string, string> = {
  durability: 'HP',
  cannon: 'FP',
  torpedo: 'TRP',
  antiaircraft: 'AA',
  air: 'AVI',
  reload: 'RLD',
  hit: 'ACC',
  dodge: 'EVA',
  speed: 'SPD',
  luck: 'LCK',
  antisub: 'ASW',
}

// a drop picked to grant or attach, drawn with its icon
export interface Queued {
  type: number
  id: number
  count: number
  name: string
  icon?: string
  rarity?: number
}

// the drop types a Queued's icon draws as a ship's frame
export const shipFramed = (type: number) => type === drop.ship
