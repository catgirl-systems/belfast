package admin

import (
	"context"
	"net/http"
	"sort"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/game"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// furnitureCaps is furniture_data_template's count: how many of a piece a
// backyard can own.
var furnitureCaps = sync.OnceValues(func() (map[uint32]uint32, error) {
	rows, err := gamedata.Table[struct {
		Count uint32 `json:"count"`
	}]("furniture_data_template")
	if err != nil {
		return nil, err
	}
	caps := make(map[uint32]uint32, len(rows))
	for id, row := range rows {
		caps[id] = row.Count
	}
	return caps, nil
})

// handleGrantAll grants a whole catalogue in one transaction, topping each
// entry up to a count (what the commander has already counts):
//   - furniture (5): every named piece, up to its table count (count, when
//     given, caps it);
//   - gear (3): every +0 piece, count each (default 1); with best, one per
//     name, its highest rarity at its last enhancement;
//   - items (2): every storable named item, count each (default 1), at most
//     its max_num.
//
// The dock and the gear depot must hold what it adds.
func (server *Server) handleGrantAll(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Type  uint32 `json:"type"`
		Count uint32 `json:"count"`
		Best  bool   `json:"best"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	tables, err := loadPlayerTables()
	if err != nil {
		return nil, err
	}
	ctx, id := r.Context(), commander.ID
	owned := map[uint32]uint32{}
	target := func(uint32) uint32 { return max(body.Count, 1) }
	var ids []uint32
	named := func(item uint32) bool {
		name := server.names[body.Type][item].Name
		return name != "" && name != "0" && game.Storable(body.Type, item)
	}
	switch body.Type {
	case dropFurniture:
		caps, err := furnitureCaps()
		if err != nil {
			return nil, err
		}
		rows, err := server.Store.ListFurniture(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			owned[row.FurnitureID] = row.Count
		}
		target = func(item uint32) uint32 {
			limit := max(caps[item], 1)
			if body.Count != 0 {
				return min(body.Count, limit)
			}
			return limit
		}
		for item := range server.names[dropFurniture] {
			if named(item) {
				ids = append(ids, item)
			}
		}
	case dropEquip:
		rows, err := server.Store.ListEquipments(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			owned[row.EquipID] = row.Count
		}
		best := map[string]uint32{}
		for item := range server.names[dropEquip] {
			if tables.equipLevels[item] != 1 || !named(item) {
				continue
			}
			if !body.Best {
				ids = append(ids, item)
				continue
			}
			name := server.names[dropEquip][item]
			known, ok := best[name.Name]
			if !ok || name.Rarity > server.names[dropEquip][known].Rarity || (name.Rarity == server.names[dropEquip][known].Rarity && item > known) {
				best[name.Name] = item
			}
		}
		for _, item := range best {
			// its last enhancement, the end of its next chain
			for next := tables.equipNext[item]; next != 0 && game.Storable(dropEquip, next); next = tables.equipNext[item] {
				item = next
			}
			ids = append(ids, item)
		}
	case dropItem:
		rows, err := server.Store.ListItems(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			owned[row.ItemID] = row.Count
		}
		target = func(item uint32) uint32 {
			if limit := tables.itemCaps[item]; limit > 0 {
				return min(max(body.Count, 1), limit)
			}
			return max(body.Count, 1)
		}
		for item := range server.names[dropItem] {
			if named(item) {
				ids = append(ids, item)
			}
		}
	default:
		return nil, badRequest("type: want 2 (items), 3 (gear) or 5 (furniture)")
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var drops []*protobuf.DROPINFO
	var pieces uint32
	for _, item := range ids {
		if want := target(item); owned[item] < want {
			drops = append(drops, &protobuf.DROPINFO{Type: proto.Uint32(body.Type), Id: proto.Uint32(item), Number: proto.Uint32(want - owned[item])})
			pieces += want - owned[item]
		}
	}
	if len(drops) == 0 {
		return map[string]any{"kicked": false, "granted": 0, "pieces": 0}, nil
	}
	was, err := server.Game.GrantWithinBags(ctx, id, drops)
	if err != nil {
		return nil, err
	}
	return map[string]any{"kicked": was, "granted": len(drops), "pieces": pieces}, nil
}

// handleBagMax sets a commander's dock, gear depot and meowfficer bag
// capacities (game.BagLimits).
func (server *Server) handleBagMax(r *http.Request) (any, error) {
	type body struct {
		ShipBagMax      uint32 `json:"ship_bag_max"`
		EquipBagMax     uint32 `json:"equip_bag_max"`
		CommanderBagMax uint32 `json:"commander_bag_max"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetBagMax(ctx, id, b.ShipBagMax, b.EquipBagMax, b.CommanderBagMax)
	})
}
