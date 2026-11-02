package admin

import (
	"sort"
	"strings"

	"github.com/ggmolly/belfast/internal/gamedata"
)

// DROP_TYPE_* (const.lua) the back-office names and grants.
const (
	dropResource  = 1
	dropItem      = 2
	dropEquip     = 3
	dropShip      = 4
	dropFurniture = 5
	dropSkin      = 7
	dropVItem     = 8
	// DROP_TYPE_ISLAND_ITEM and DROP_TYPE_ISLAND_DRESS: the island's
	// warehouse items and outfits, stored in its blob
	dropIslandItem  = 41
	dropIslandDress = 46
)

// named is a config table row's name, rarity and icon: the client bundle
// its picture is in, served by /api/assets/<icon>.png (see assets).
type named struct {
	Name   string `json:"name"`
	Rarity uint32 `json:"rarity"`
	Icon   string `json:"icon,omitempty"`
}

// tableRow is what the config tables say of an icon: an icon field, or the
// skin (ships) or virtual item (resources) that has the picture.
type tableRow struct {
	Name     string `json:"name"`
	Rarity   uint32 `json:"rarity"`
	Icon     string `json:"icon"`
	SkinID   uint32 `json:"skin_id"`
	Painting string `json:"painting"`
	ItemID   uint32 `json:"itemid"`
	// equip_data_template: an upgraded piece's +0
	Base uint32 `json:"base"`
	// island_dress_template's rarity
	Quality uint32 `json:"quality"`
}

// names are the config tables' names of what a commander owns, by drop type.
type names map[uint32]map[uint32]named

func (names *names) load() error {
	tables := map[uint32]string{
		dropResource:    "player_resource",
		dropItem:        "item_data_statistics",
		dropEquip:       "equip_data_statistics",
		dropShip:        "ship_data_statistics",
		dropFurniture:   "furniture_data_template",
		dropSkin:        "ship_skin_template",
		dropVItem:       "item_virtual_data_statistics",
		dropIslandItem:  "island_item_data_template",
		dropIslandDress: "island_dress_template",
	}
	raw := make(map[uint32]map[uint32]tableRow, len(tables))
	for kind, table := range tables {
		rows, err := gamedata.Table[tableRow](table)
		if err != nil {
			return err
		}
		raw[kind] = rows
	}
	// an upgraded piece of gear (+N) is named, drawn and ranked as its +0:
	// its equip_data_statistics row only lists what differs, when it has one
	upgrades, err := gamedata.Table[tableRow]("equip_data_template")
	if err != nil {
		return err
	}
	for id, upgrade := range upgrades {
		if base, ok := raw[dropEquip][upgrade.Base]; ok && upgrade.Base != id {
			row := raw[dropEquip][id]
			row.Name, row.Rarity, row.Icon = base.Name, base.Rarity, base.Icon
			raw[dropEquip][id] = row
		}
	}
	*names = make(map[uint32]map[uint32]named, len(tables))
	for kind, rows := range raw {
		byID := make(map[uint32]named, len(rows))
		for id, row := range rows {
			if kind == dropIslandDress {
				row.Rarity = row.Quality
			}
			row.Icon = iconBundle(kind, row, raw)
			byID[id] = namedRow(row)
		}
		(*names)[kind] = byID
	}
	return nil
}

func namedRow(row tableRow) named {
	return named{Name: row.Name, Rarity: row.Rarity, Icon: row.Icon}
}

// iconBundle is the bundle path of a row's icon, as the client loads it:
// item icons are "Props/20001" (bundle props/20001), gear icons are the
// equips/ bundle's names, ships and skins show their painting's squareicon,
// furniture its furnitureicon, and a resource the icon of its virtual item.
func iconBundle(kind uint32, row tableRow, raw map[uint32]map[uint32]tableRow) string {
	switch kind {
	case dropItem, dropVItem:
		return strings.ToLower(row.Icon)
	case dropEquip:
		if row.Icon != "" {
			return "equips/" + strings.ToLower(row.Icon)
		}
	case dropShip:
		if painting := raw[dropSkin][row.SkinID].Painting; painting != "" {
			return "squareicon/" + strings.ToLower(painting)
		}
	case dropSkin:
		if row.Painting != "" {
			return "squareicon/" + strings.ToLower(row.Painting)
		}
	case dropFurniture:
		if row.Icon != "" {
			return "furnitureicon/" + strings.ToLower(row.Icon)
		}
	case dropResource:
		return strings.ToLower(raw[dropVItem][row.ItemID].Icon)
	case dropIslandItem:
		// its icon field, a bundle path ("Islandprops/1")
		return strings.ToLower(row.Icon)
	case dropIslandDress:
		// an atlas: IslandDressCard loads island/IslandDressIcon/<icon>
		if row.Icon != "" {
			return "island/islanddressicon/" + strings.ToLower(row.Icon)
		}
	}
	return ""
}

// shown is how a drop of kind is shown, as Item:getConfigData looks an item
// up: in item_virtual_data_statistics first, then item_data_statistics
// (rewards such as 59001 Coins are items only the virtual table has). For
// display only: what can be granted or set stays the item table's.
func (names names) shown(kind, id uint32) named {
	if kind == dropItem {
		if row, ok := names[dropVItem][id]; ok {
			return row
		}
	}
	return names[kind][id]
}

// name is what kind's id is called, empty when the tables don't say.
func (names names) name(kind, id uint32) string {
	return names[kind][id].Name
}

// shipIcon is an owned ship's picture: the squareicon of the skin it wears,
// else its template's (skin 0 is the template's own).
func (names names) shipIcon(templateID, skinID uint32) string {
	if icon := names[dropSkin][skinID].Icon; skinID != 0 && icon != "" {
		return icon
	}
	return names[dropShip][templateID].Icon
}

// catalogEntry is one thing of a drop type, for picking what to grant.
type catalogEntry struct {
	ID     uint32 `json:"id"`
	Name   string `json:"name"`
	Rarity uint32 `json:"rarity"`
	Icon   string `json:"icon,omitempty"`
}

// search lists up to limit things of kind whose name or id holds query.
func (names names) search(kind uint32, query string, limit int) []catalogEntry {
	query = strings.ToLower(strings.TrimSpace(query))
	var found []catalogEntry
	for id, row := range names[kind] {
		if row.Name == "" {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(row.Name), query) || strings.Contains(itoa(id), query) {
			found = append(found, catalogEntry{ID: id, Name: row.Name, Rarity: row.Rarity, Icon: row.Icon})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	if len(found) > limit {
		found = found[:limit]
	}
	return found
}
