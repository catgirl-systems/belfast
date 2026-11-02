// The Archives screen's edits (internal/admin archives.go), cached under
// keys.screen(id, 'archives', 'details'); the screen itself is
// LifeArchives (lib/life).

export interface ArchiveDrop {
  type: number
  id: number
  count: number
  name?: string
  rarity?: number
  icon?: string
}

// an archive entry with the bounds an edit keeps
export interface ArchiveShip {
  group: number
  name: string
  icon?: string
  rarity: number
  level: number
  intimacy: number
  star: number
  married: boolean
  liked: boolean
  retrofit: boolean
  // the best ship of the group owned, 0 none
  ship: number
  min_level: number
  min_intimacy: number
  min_star: number
  min_married: boolean
  max_level: number
  max_intimacy: number
  max_intimacy_oath: number
  max_star: number
  can_retrofit: boolean
}

export interface ArchiveGroup {
  group: number
  name: string
  icon?: string
  rarity: number
}

export interface ArchiveStep {
  index: number
  level: number
  claimed: boolean
  award: ArchiveDrop
}

export interface ArchiveCollection {
  id: number
  name: string
  stars: number
  claimed: number
  steps: ArchiveStep[]
}

export interface ArchiveTier {
  id: number
  target: number
  progress: number
  claimed: boolean
  reached: boolean
}

export interface ArchiveMedal {
  id: number
  name: string
  hidden: boolean
  tiers: ArchiveTier[]
}

export interface ArchiveFrame {
  type: number
  id: number
  name: string
  icon?: string
  worn: boolean
}

export interface ArchiveMemory {
  id: number
  title: string
  open: boolean
  seen: boolean
}

export interface ArchiveMemoryGroup {
  id: number
  title: string
  seen: number
  total: number
}

export interface ArchiveCryptolalia {
  id: number
  name: string
  unlocked: boolean
}

export interface ArchiveDetails {
  ships: ArchiveShip[]
  groups: ArchiveGroup[]
  collections: ArchiveCollection[]
  medals: ArchiveMedal[]
  display: number[]
  display_max: number
  frames: ArchiveFrame[]
  catalog: ArchiveFrame[]
  memories: ArchiveMemoryGroup[] | null
  memories_error?: string
  music: { album: number; mode: number; modes: number; albums: { id: number; name: string }[] }
  skins: Record<number, { skin: number; name: string }>
}

// the frames' drop types (const.lua DROP_TYPE_*)
export const frameKinds = [
  { type: 14, label: 'Icon', color: 'bg-[#4f7fe0]' },
  { type: 15, label: 'Chat', color: 'bg-[#2f9e5c]' },
  { type: 29, label: 'Wallpaper', color: 'bg-[#a05cc8]' },
]

// the music player's albums beyond the music_album ones, and its loop types
export const musicNone = 0
export const musicFavor = 999
export const musicModes = ['List', 'Shuffle', 'One song']
