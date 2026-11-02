package game

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// WorldMapAttachment types (WORLDITEMINFO.item_type).
const (
	worldAttachBox       = 2  // box_data_template
	worldAttachEnemy     = 6  // expedition_data_template / world_expedition_data
	worldAttachBoss      = 8  // a boss enemy, with boss_hp
	worldAttachArtifact  = 10 // world_event_data scenery
	worldAttachEnemyAI   = 12 // an enemy that moves, with boss_hp
	worldAttachFleet     = 13 // a fleet of the player (item_id the fleet id)
	worldAttachTransport = 17 // friendly_data_template, hp in item_data
	worldAttachEvent     = 22 // world_event_data
	worldAttachTrap      = 23 // world_trap_data
)

// MAPINFO.state_flag values (WorldProxy:NetUpdateMap).
const (
	worldMapFlagClear      = 1 // the map is cleared (WorldMap:UpdateClearFlag)
	worldMapFlagVision     = 2 // its attachments are visible (UpdateVisionFlag)
	worldMapFlagDiscovered = 3 // every cell discovered
)

// worldMap is a map of a world (MAPINFO): the template it is played on and
// what is on it. Fleets aren't attachments here: they are the world's
// (worldState.Fleets), put on the active map's cells by posInfos.
type worldMap struct {
	RandomID   uint32 `json:"random_id"`
	TemplateID uint32 `json:"template_id"`
	// Generation is the generator's version the map was made by
	// (worldMapGeneration)
	Generation uint32 `json:"generation,omitempty"`
	Clear      bool   `json:"clear,omitempty"`
	Vision     bool   `json:"vision,omitempty"`
	// AllDiscovered stands for every cell in Discovered (state flag 3).
	AllDiscovered bool        `json:"all_discovered,omitempty"`
	Discovered    [][2]uint32 `json:"discovered,omitempty"`

	Attachments []*worldAttachment `json:"attachments,omitempty"`
	// Lands are the cells' terrain (land_list), the template's to begin
	// with, changed by events afterwards.
	Lands []worldLand `json:"lands,omitempty"`
	// Stronghold is a Siren Stronghold's structures (worldstronghold.go).
	Stronghold *worldStrongholdMap `json:"stronghold,omitempty"`
}

// worldAttachment is what stands on a cell (WORLDITEMINFO).
type worldAttachment struct {
	Row    uint32 `json:"row"`
	Column uint32 `json:"column"`
	Type   uint32 `json:"type"`
	ID     uint32 `json:"id"`
	// Flag 1 is gone: a beaten enemy, an opened box (WorldMapAttachment:IsAlive)
	Flag uint32 `json:"flag,omitempty"`
	// Data is the attachment's counter: an enemy is alive while it isn't 0,
	// a transport fleet's hp, an event's progress
	Data   uint32   `json:"data,omitempty"`
	Buffs  []uint32 `json:"buffs,omitempty"`
	BossHP uint32   `json:"boss_hp,omitempty"` // 1/10000, TypeEnemyAI and TypeBoss
	// Effects are an event's effects still to run (effect_list,
	// WorldMapAttachment:GetEventEffect is the first)
	Effects []uint32 `json:"effects,omitempty"`
}

// worldLand is a cell's terrain (LANDINFO, NetTerrainUpdate).
type worldLand struct {
	Row    uint32 `json:"row"`
	Column uint32 `json:"column"`
	Type   uint32 `json:"type"` // WorldMapCell.Terrain*
	// DirRow / DirColumn is the stream's or wind's direction, -1/0/1
	// (sent as dir {row+1, column+1}, WorldConst.ParseConfigDir)
	DirRow    int32  `json:"dir_row,omitempty"`
	DirColumn int32  `json:"dir_column,omitempty"`
	Distance  uint32 `json:"distance,omitempty"` // a wind's strength, a fog's or poison's
}

// cellAttachments are the attachments on a cell.
func (m *worldMap) cellAttachments(row, column uint32) []*worldAttachment {
	var list []*worldAttachment
	for _, attachment := range m.Attachments {
		if attachment.Row == row && attachment.Column == column {
			list = append(list, attachment)
		}
	}
	return list
}

// attachment finds the attachment of a type and id on a cell, nil for none.
func (m *worldMap) attachment(row, column, kind, id uint32) *worldAttachment {
	for _, attachment := range m.Attachments {
		if attachment.Row == row && attachment.Column == column && attachment.Type == kind && attachment.ID == id {
			return attachment
		}
	}
	return nil
}

// addAttachment puts an attachment on the map, unless the cell already has
// one of the same type and id (WorldMapCell:AddAttachment asserts there is
// none); it tells whether it was added.
func (m *worldMap) addAttachment(attachment *worldAttachment) bool {
	if m.attachment(attachment.Row, attachment.Column, attachment.Type, attachment.ID) != nil {
		return false
	}
	m.Attachments = append(m.Attachments, attachment)
	return true
}

// discover marks cells discovered; it returns those that weren't.
func (m *worldMap) discover(cells ...[2]uint32) [][2]uint32 {
	if m.AllDiscovered {
		return nil
	}
	var added [][2]uint32
	for _, cell := range cells {
		if !slices.Contains(m.Discovered, cell) {
			m.Discovered = append(m.Discovered, cell)
			added = append(added, cell)
		}
	}
	return added
}

func worldItemInfo(attachment *worldAttachment) *protobuf.WORLDITEMINFO {
	info := &protobuf.WORLDITEMINFO{
		ItemType:   proto.Uint32(attachment.Type),
		ItemId:     proto.Uint32(attachment.ID),
		ItemFlag:   proto.Uint32(attachment.Flag),
		ItemData:   proto.Uint32(attachment.Data),
		BuffList:   attachment.Buffs,
		EffectList: attachment.Effects,
	}
	if attachment.Type == worldAttachBoss || attachment.Type == worldAttachEnemyAI {
		info.BossHp = proto.Uint32(attachment.BossHP)
	}
	return info
}

// posInfo is a cell as WORLDPOSINFO: its attachments, and the fleets of
// fleets standing on it. WorldProxy:UpdateMapAttachmentCells drops what a
// cell had that isn't in the list, so a cell is always sent whole (an
// empty list clears it).
func (m *worldMap) posInfo(row, column uint32, fleets []*worldFleet) *protobuf.WORLDPOSINFO {
	info := &protobuf.WORLDPOSINFO{Pos: worldCellPos(row, column)}
	for _, attachment := range m.cellAttachments(row, column) {
		info.ItemList = append(info.ItemList, worldItemInfo(attachment))
	}
	for _, fleet := range fleets {
		if fleet.Row == row && fleet.Column == column {
			info.ItemList = append(info.ItemList, &protobuf.WORLDITEMINFO{ItemType: proto.Uint32(worldAttachFleet), ItemId: proto.Uint32(fleet.ID)})
		}
	}
	return info
}

// posInfos are every cell with something on it (MAPINFO.pos_list,
// SC_33105), fleets included for the active map.
func (m *worldMap) posInfos(fleets []*worldFleet) []*protobuf.WORLDPOSINFO {
	var cells [][2]uint32
	add := func(row, column uint32) {
		if cell := [2]uint32{row, column}; !slices.Contains(cells, cell) {
			cells = append(cells, cell)
		}
	}
	for _, attachment := range m.Attachments {
		add(attachment.Row, attachment.Column)
	}
	for _, fleet := range fleets {
		add(fleet.Row, fleet.Column)
	}
	var infos []*protobuf.WORLDPOSINFO
	for _, cell := range cells {
		infos = append(infos, m.posInfo(cell[0], cell[1], fleets))
	}
	return infos
}

// landInfos are the map's terrain (MAPINFO.land_list, SC_33104.land_list).
func (m *worldMap) landInfos() []*protobuf.LANDINFO {
	var infos []*protobuf.LANDINFO
	for _, land := range m.Lands {
		infos = append(infos, &protobuf.LANDINFO{
			Pos:      worldCellPos(land.Row, land.Column),
			Type:     proto.Uint32(land.Type),
			Dir:      worldCellPos(uint32(land.DirRow+1), uint32(land.DirColumn+1)),
			Distance: proto.Uint32(land.Distance),
		})
	}
	return infos
}

// landInfo is a cell's terrain as LANDINFO, type 0 when it has none (a
// terrain cleared: NetTerrainUpdate sets the cell's to it).
func (m *worldMap) landInfo(cell [2]uint32) *protobuf.LANDINFO {
	land, ok := m.land(cell)
	if !ok {
		return &protobuf.LANDINFO{Pos: worldCellPos(cell[0], cell[1]), Type: proto.Uint32(0), Dir: worldCellPos(1, 1), Distance: proto.Uint32(0)}
	}
	return &protobuf.LANDINFO{
		Pos:      worldCellPos(land.Row, land.Column),
		Type:     proto.Uint32(land.Type),
		Dir:      worldCellPos(uint32(land.DirRow+1), uint32(land.DirColumn+1)),
		Distance: proto.Uint32(land.Distance),
	}
}

// mapID is the map as WORLDMAPID.
func (m *worldMap) mapID() *protobuf.WORLDMAPID {
	return &protobuf.WORLDMAPID{RandomId: proto.Uint32(m.RandomID), TemplateId: proto.Uint32(m.TemplateID)}
}

// mapInfo is the map as MAPINFO (WorldProxy:NetUpdateMap); fleets are the
// world's when it is the active map.
func (m *worldMap) mapInfo(fleets []*worldFleet) *protobuf.MAPINFO {
	info := &protobuf.MAPINFO{
		Id:       m.mapID(),
		LandList: m.landInfos(),
		PosList:  m.posInfos(fleets),
	}
	if m.Clear {
		info.StateFlag = append(info.StateFlag, worldMapFlagClear)
	}
	if m.Vision {
		info.StateFlag = append(info.StateFlag, worldMapFlagVision)
	}
	if m.AllDiscovered {
		info.StateFlag = append(info.StateFlag, worldMapFlagDiscovered)
	} else {
		for _, cell := range m.Discovered {
			info.CellList = append(info.CellList, &protobuf.CHAPTERCELLINFO_P33{Pos: worldCellPos(cell[0], cell[1]), Discovered: proto.Uint32(1)})
		}
	}
	return info
}

// loadWorldMap reads a map of the commander's world; nil when it was never
// stored.
func loadWorldMap(ctx context.Context, queries *db.Queries, commanderID, randomID uint32) (*worldMap, error) {
	raw, err := queries.GetWorldMap(ctx, db.GetWorldMapParams{CommanderID: commanderID, RandomID: randomID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m := &worldMap{}
	return m, json.Unmarshal(raw, m)
}

// worldMap is the world's map randomID: the one stored, else one generated
// (and stored) on its template in the world's Maps or one drawn for the
// world's progress. Storing a map doesn't put it in Maps (chapter_list,
// the maps whose enter_cost is paid): the fleets entering it do
// (enterMap). A map stored by an older generator (worldMapGeneration) is
// made again on its template while nothing was done on it: never the
// active map nor a secured one. nil when randomID isn't a
// world_chapter_random map.
func (tx *Tx) worldMap(state *worldState, randomID uint32) (*worldMap, error) {
	m, err := loadWorldMap(tx.ctx, tx.Queries, tx.commanderID, randomID)
	if err != nil {
		return nil, err
	}
	if m != nil && !m.outdated(state) {
		if repairArrivalEvents(m, state.Count.Progress, state.Fleets, worldRand(worldSeed(tx.commanderID, state.Count.Activations, m.RandomID))) {
			return m, tx.saveWorldMap(m)
		}
		if m.RandomID == worldForwardBase && refreshForwardBase(m, state, worldRand(worldSeed(tx.commanderID, state.Count.Activations, m.RandomID))) {
			return m, tx.saveWorldMap(m)
		}
		return m, nil
	}
	if _, ok := worldRandomMaps[randomID]; !ok {
		return nil, nil
	}
	seed := worldSeed(tx.commanderID, state.Count.Activations, randomID)
	templateID := state.mapTemplate(randomID)
	if m != nil {
		templateID = m.TemplateID
	}
	if templateID == 0 {
		templateID = drawWorldTemplate(randomID, state.Count.Progress, seed)
	}
	old := m
	m = generateWorldMap(randomID, templateID, seed)
	if old != nil {
		// the events tasks put there (task_event) stay
		for _, a := range old.Attachments {
			if a.Type == worldAttachEvent && worldTaskEvents[a.ID] {
				m.addAttachment(a)
			}
		}
	}
	if m.RandomID == worldForwardBase {
		refreshForwardBase(m, state, worldRand(seed))
	}
	return m, tx.saveWorldMap(m)
}

// outdated tells whether a stored map is to be made again (worldMap).
func (m *worldMap) outdated(state *worldState) bool {
	return m.Generation < worldMapGeneration && worldEventFamily(m.RandomID) == nil && m.RandomID != state.MapID &&
		!slices.Contains(state.CleanChapters, m.RandomID) && worldTemplates[m.TemplateID] != nil
}

// enterMap puts a map the fleets enter in the world's Maps (chapter_list).
func (state *worldState) enterMap(m *worldMap) {
	if state.mapTemplate(m.RandomID) == 0 {
		state.Maps = append(state.Maps, worldMapID{RandomID: m.RandomID, TemplateID: m.TemplateID})
	}
}

// saveWorldMap stores a map of the commander's world.
func (tx *Tx) saveWorldMap(m *worldMap) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tx.SaveWorldMap(tx.ctx, db.SaveWorldMapParams{CommanderID: tx.commanderID, RandomID: m.RandomID, State: raw})
}
