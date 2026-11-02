package admin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/ggmolly/belfast/internal/game"
	"github.com/ggmolly/belfast/internal/gamedata"
)

// registerDock registers the API of a commander's ships, fleets and meowfficers.
func (server *Server) registerDock(api apiFunc) {
	api("GET /api/commanders/{id}/dock", server.handleDock)
	api("GET /api/commanders/{id}/dock/{ship}", server.handleDockShip)
	api("POST /api/commanders/{id}/dock/{ship}", server.handleEditShip)
	api("DELETE /api/commanders/{id}/dock/{ship}", server.handleRemoveShip)
	api("POST /api/commanders/{id}/dock/{ship}/strength", server.handleMaxShipStrength)
	api("POST /api/commanders/{id}/dock/strength", server.handleMaxDockStrength)
	api("POST /api/commanders/{id}/dock/gear", server.handleDockGear)
	api("GET /api/commanders/{id}/fleets", server.handleFleets)
	api("POST /api/commanders/{id}/fleets/{fleet}", server.handleEditFleet)
	api("DELETE /api/commanders/{id}/fleets/{fleet}", server.handleClearFleet)
	api("DELETE /api/commanders/{id}/elite-fleets/{formation}", server.handleClearEliteFleet)
	api("POST /api/commanders/{id}/elite-fleets/{formation}/{kind}/{index}", server.handleEditEliteTeam)
	api("DELETE /api/commanders/{id}/activity-fleets/{activity}", server.handleClearActivityFleet)
	api("POST /api/commanders/{id}/activity-fleets/{activity}/{fleet}", server.handleEditActivityFleet)
	api("GET /api/commanders/{id}/meowfficers", server.handleMeowfficers)
	api("POST /api/commanders/{id}/meowfficers", server.handleGrantMeowfficer)
	api("POST /api/commanders/{id}/meowfficers/{cat}", server.handleEditMeowfficer)
	api("POST /api/commanders/{id}/meowfficer-boxes/{box}/finish", server.handleFinishMeowfficerBox)
	api("DELETE /api/commanders/{id}/meowfficer-boxes/{box}", server.handleEmptyMeowfficerBox)
	api("POST /api/commanders/{id}/cattery/clean", server.handleFillCatteryClean)
	api("POST /api/commanders/{id}/cattery/reset-ops", server.handleResetCatteryOps)
	api("DELETE /api/commanders/{id}/meowfficer-presets/{preset}", server.handleClearMeowfficerPreset)
}

// The names the dock screens show that names doesn't keep: ship types,
// nations, skins, skills, meowfficers and chapters. Read on first use.
type dockNameTables struct {
	shipTypes  map[uint32]string // ship_data_by_type type_name
	nations    map[uint32]string // Nation.Nation2Name's gametip
	skins      map[uint32]dockSkin
	skills     map[uint32]string
	cats       map[uint32]dockCat
	catSkills  map[uint32]dockCatSkill
	talents    map[uint32]string
	boxItems   map[uint32]uint32 // commander_data_create_material use_item by pool
	formations map[uint32][]string
	chapters   map[uint32]string    // chapter_template chapter_name
	equips     map[uint32]dockEquip // equip_data_template base and level
}

type dockSkin struct {
	Name      string `json:"name"`
	Painting  string `json:"painting"`
	ShipGroup uint32 `json:"ship_group"`
}

type dockEquip struct {
	Base  uint32 `json:"base"`
	Level uint32 `json:"level"`
}

type dockCat struct {
	Name        string `json:"name"`
	Painting    string `json:"painting"`
	Nationality uint32 `json:"nationality"`
	Rarity      uint32 `json:"rarity"`
}

type dockCatSkill struct {
	Name  string `json:"name"`
	Level uint32 `json:"lv"`
}

// nationTips are the gametip keys of Nation.Nation2Name
// (model/const/nation.lua).
var nationTips = map[uint32]string{
	0: "word_shipNation_other", 1: "word_shipNation_baiYing", 2: "word_shipNation_huangJia", 3: "word_shipNation_chongYing",
	4: "word_shipNation_tieXue", 5: "word_shipNation_dongHuang", 6: "word_shipNation_saDing", 7: "word_shipNation_beiLian",
	8: "word_shipNation_ziyou", 9: "word_shipNation_weixi", 10: "word_shipNation_yuanwei", 11: "word_shipNation_yujinwangguo",
	12: "word_shipNation_jinghuanlianmeng", 96: "word_shipNation_mot", 97: "word_shipNation_meta", 98: "word_shipNation_other",
	101: "word_shipNation_np", 102: "word_shipNation_bili", 103: "word_shipNation_um", 104: "word_shipNation_ai",
	105: "word_shipNation_holo", 106: "word_shipNation_doa", 107: "word_shipNation_imas", 108: "word_shipNation_ssss",
	109: "word_shipNation_ryza", 110: "word_shipNation_senran", 111: "word_shipNation_tolove", 112: "word_shipNation_brs",
	113: "word_shipNation_yumia", 114: "word_shipNation_danmachi", 115: "word_shipNation_dal", 117: "word_shipNation_nierautomata",
}

var dockNames = sync.OnceValues(func() (*dockNameTables, error) {
	tables := &dockNameTables{}
	types, err := gamedata.Table[struct {
		TypeName string `json:"type_name"`
	}]("ship_data_by_type")
	if err != nil {
		return nil, err
	}
	tables.shipTypes = map[uint32]string{}
	for id, row := range types {
		tables.shipTypes[id] = row.TypeName
	}
	tips, err := gamedata.Gametip()
	if err != nil {
		return nil, err
	}
	tables.nations = map[uint32]string{}
	for id, key := range nationTips {
		tables.nations[id] = tips[key]
	}
	if tables.skins, err = gamedata.Table[dockSkin]("ship_skin_template"); err != nil {
		return nil, err
	}
	skills, err := gamedata.Table[struct {
		Name string `json:"name"`
	}]("skill_data_template")
	if err != nil {
		return nil, err
	}
	tables.skills = map[uint32]string{}
	for id, row := range skills {
		tables.skills[id] = strings.TrimSpace(row.Name)
	}
	if tables.cats, err = gamedata.Table[dockCat]("commander_data_template"); err != nil {
		return nil, err
	}
	if tables.catSkills, err = gamedata.Table[dockCatSkill]("commander_skill_template"); err != nil {
		return nil, err
	}
	talents, err := gamedata.Table[struct {
		Name string `json:"name"`
	}]("commander_ability_template")
	if err != nil {
		return nil, err
	}
	tables.talents = map[uint32]string{}
	for id, row := range talents {
		tables.talents[id] = row.Name
	}
	pools, err := gamedata.Table[struct {
		UseItem uint32 `json:"use_item"`
	}]("commander_data_create_material")
	if err != nil {
		return nil, err
	}
	tables.boxItems = map[uint32]uint32{}
	for id, row := range pools {
		tables.boxItems[id] = row.UseItem
	}
	chapters, err := gamedata.Table[struct {
		ChapterName string `json:"chapter_name"`
		Formation   uint32 `json:"formation"`
	}]("chapter_template")
	if err != nil {
		return nil, err
	}
	ids := make([]uint32, 0, len(chapters))
	for id := range chapters {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if tables.equips, err = gamedata.Table[dockEquip]("equip_data_template"); err != nil {
		return nil, err
	}
	tables.formations = map[uint32][]string{}
	tables.chapters = map[uint32]string{}
	for _, id := range ids {
		row := chapters[id]
		tables.chapters[id] = row.ChapterName
		if row.Formation != 0 && row.ChapterName != "" {
			tables.formations[row.Formation] = append(tables.formations[row.Formation], row.ChapterName)
		}
	}
	return tables, nil
})

// shipyardIcon is the dock card art of a skin (the client's shipyardicon
// bundle of its painting).
func (tables *dockNameTables) shipyardIcon(skin uint32) string {
	if painting := tables.skins[skin].Painting; painting != "" {
		return "shipyardicon/" + strings.ToLower(painting)
	}
	return ""
}

type dockShipRow struct {
	ID          uint32 `json:"id"`
	TemplateID  uint32 `json:"template_id"`
	Name        string `json:"name"`
	Rarity      uint32 `json:"rarity"`
	Star        uint32 `json:"star"`
	MaxStar     uint32 `json:"max_star"`
	Level       uint32 `json:"level"`
	Locked      bool   `json:"locked"`
	Married     bool   `json:"married"`
	Fleet       uint32 `json:"fleet"`
	Type        string `json:"type"`
	Team        string `json:"team"`
	Nation      string `json:"nation"`
	SkinID      uint32 `json:"skin_id"`
	Icon        string `json:"icon,omitempty"`
	SquareIcon  string `json:"square_icon,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	Nationality uint32 `json:"-"`
}

func (server *Server) dockShipRow(tables *dockNameTables, ship game.DockShip) dockShipRow {
	row := dockShipRow{
		ID: ship.ID, TemplateID: ship.TemplateID, Name: ship.Name, Rarity: ship.Rarity, Star: ship.Star, MaxStar: ship.MaxStar,
		Level: ship.Level, Locked: ship.Locked, Married: ship.Married, Fleet: ship.Fleet, Type: tables.shipTypes[ship.Type],
		Team: ship.Team, Nation: tables.nations[ship.Nationality], SkinID: ship.SkinID, CreatedAt: ship.CreatedAt,
		Icon: tables.shipyardIcon(ship.SkinID),
	}
	if painting := tables.skins[ship.SkinID].Painting; painting != "" {
		row.SquareIcon = "squareicon/" + strings.ToLower(painting)
	}
	if row.Name == "" {
		row.Name = server.names.name(dropShip, ship.TemplateID)
	}
	if row.Rarity == 0 {
		row.Rarity = server.names[dropShip][ship.TemplateID].Rarity
	}
	return row
}

// handleDock lists a commander's ships, the dock's capacity.
func (server *Server) handleDock(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	tables, err := dockNames()
	if err != nil {
		return nil, err
	}
	ships, err := server.Game.DockShips(r.Context(), commander.ID)
	if err != nil {
		return nil, err
	}
	rows := make([]dockShipRow, len(ships))
	for i, ship := range ships {
		rows[i] = server.dockShipRow(tables, ship)
	}
	return map[string]any{"capacity": commander.ShipBagMax, "ships": rows}, nil
}

type namedRef struct {
	ID     uint32 `json:"id"`
	Name   string `json:"name"`
	Rarity uint32 `json:"rarity,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Count  uint32 `json:"count,omitempty"`
}

type shipDetailJSON struct {
	dockShipRow
	Exp     uint32 `json:"exp"`
	ExpNext uint32 `json:"exp_next"`
	ExpMax  uint32 `json:"exp_max"`
	// exp_max at each level from 1 to level_cap
	ExpMaxes    []uint32       `json:"exp_maxes"`
	MaxLevel    uint32         `json:"max_level"`
	LevelCap    uint32         `json:"level_cap"`
	Intimacy    uint32         `json:"intimacy"`
	IntimacyMax uint32         `json:"intimacy_max"`
	Energy      uint32         `json:"energy"`
	EnergyMax   uint32         `json:"energy_max"`
	Propose     uint32         `json:"propose"`
	Group       uint32         `json:"group"`
	DefaultSkin uint32         `json:"default_skin"`
	Stars       []shipStarJSON `json:"stars"`
	Skins       []namedRef     `json:"skins"`
	Retrofit    *retrofitJSON  `json:"retrofit"`
	Fleets      []shipFleet    `json:"fleets"`
	Slot        uint32         `json:"slot"`
	Skills      []shipSkill    `json:"skills"`
	Gear        []gearSlot     `json:"gear"`
	Stats       []shipStat     `json:"stats"`
	InChapter   bool           `json:"in_chapter"`
}

type shipStarJSON struct {
	TemplateID uint32 `json:"template_id"`
	Star       uint32 `json:"star"`
	MaxLevel   uint32 `json:"max_level"`
}

type retrofitJSON struct {
	Nodes int    `json:"nodes"`
	Done  int    `json:"done"`
	Full  bool   `json:"full"`
	Level uint32 `json:"level"`
	Star  uint32 `json:"star"`
}

type shipFleet struct {
	ID     uint32 `json:"id"`
	Locked bool   `json:"locked"`
}

type shipSkill struct {
	ID    uint32 `json:"id"`
	Name  string `json:"name"`
	Level uint32 `json:"level"`
	Max   uint32 `json:"max"`
	Type  uint32 `json:"type"`
}

// gearRef is a piece of equipment: an equip_data_template id, named after
// its +0 (an upgraded row of equip_data_statistics only lists what
// differs), plus its upgrades.
type gearRef struct {
	namedRef
	Plus uint32 `json:"plus"`
}

type gearSlot struct {
	Pos    uint32    `json:"pos"`
	Equip  *gearRef  `json:"equip"`
	SkinID uint32    `json:"skin_id"`
	Fits   []gearRef `json:"fits"`
}

type shipStat struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// shipPath is the {id} commander and its {ship}.
func (server *Server) shipPath(r *http.Request) (uint32, uint32, error) {
	commander, err := server.commander(r)
	if err != nil {
		return 0, 0, err
	}
	ship, err := pathUint(r, "ship")
	return commander.ID, ship, err
}

// handleDockShip is one ship with what can be edited of it.
func (server *Server) handleDockShip(r *http.Request) (any, error) {
	commanderID, shipID, err := server.shipPath(r)
	if err != nil {
		return nil, err
	}
	return server.shipDetail(r, commanderID, shipID)
}

func (server *Server) shipDetail(r *http.Request, commanderID, shipID uint32) (any, error) {
	tables, err := dockNames()
	if err != nil {
		return nil, err
	}
	detail, err := server.Game.ShipDetail(r.Context(), commanderID, shipID)
	if err != nil {
		return nil, err
	}
	depot, err := server.Store.ListEquipments(r.Context(), commanderID)
	if err != nil {
		return nil, err
	}
	counts := map[uint32]uint32{}
	for _, row := range depot {
		counts[row.EquipID] = row.Count
	}
	out := shipDetailJSON{
		dockShipRow: server.dockShipRow(tables, detail.DockShip),
		Exp:         detail.Exp, ExpNext: detail.ExpNext, ExpMax: detail.ExpMax, ExpMaxes: detail.ExpMaxes, MaxLevel: detail.MaxLevel, LevelCap: detail.LevelCap,
		Intimacy: detail.Intimacy, IntimacyMax: detail.IntimacyMax, Energy: detail.Energy, EnergyMax: detail.EnergyMax,
		Propose: detail.Propose, Slot: detail.Slot, InChapter: detail.InChapter,
		Stars: []shipStarJSON{}, Skins: []namedRef{}, Fleets: []shipFleet{}, Skills: []shipSkill{}, Gear: []gearSlot{}, Stats: []shipStat{},
	}
	for _, star := range detail.Stars {
		out.Stars = append(out.Stars, shipStarJSON(star))
	}
	for i, id := range detail.Skins {
		skin := namedRef{ID: id, Name: tables.skins[id].Name, Icon: tables.shipyardIcon(id)}
		if i == 0 {
			skin.ID = 0 // its default skin is worn as 0
			out.DefaultSkin = id
			out.Group = tables.skins[id].ShipGroup
		}
		out.Skins = append(out.Skins, skin)
	}
	if retrofit := detail.Retrofit; retrofit != nil {
		out.Retrofit = &retrofitJSON{Nodes: len(retrofit.Nodes), Done: retrofit.Done, Full: retrofit.Full, Level: retrofit.Level, Star: retrofit.Star}
	}
	for _, fleet := range detail.Fleets {
		out.Fleets = append(out.Fleets, shipFleet(fleet))
	}
	for _, skill := range detail.Skills {
		out.Skills = append(out.Skills, shipSkill{ID: skill.ID, Name: tables.skills[skill.ID], Level: skill.Level, Max: skill.Max, Type: skill.Type})
	}
	equip := func(id uint32) gearRef {
		template := tables.equips[id]
		row := server.names[dropEquip][id]
		if row.Name == "" && template.Base != 0 {
			row = server.names[dropEquip][template.Base]
		}
		return gearRef{namedRef: namedRef{ID: id, Name: row.Name, Rarity: row.Rarity, Icon: row.Icon, Count: counts[id]}, Plus: max(template.Level, 1) - 1}
	}
	for _, slot := range detail.Gear {
		gear := gearSlot{Pos: slot.Pos, SkinID: slot.SkinID, Fits: []gearRef{}}
		if slot.EquipID != 0 {
			ref := equip(slot.EquipID)
			ref.Count = 0
			gear.Equip = &ref
		}
		for _, id := range slot.Fits {
			gear.Fits = append(gear.Fits, equip(id))
		}
		sort.Slice(gear.Fits, func(i, j int) bool {
			a, b := gear.Fits[i], gear.Fits[j]
			if a.Rarity != b.Rarity {
				return a.Rarity > b.Rarity
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Plus > b.Plus
		})
		out.Gear = append(out.Gear, gear)
	}
	for _, stat := range detail.Stats {
		out.Stats = append(out.Stats, shipStat(stat))
	}
	return out, nil
}

// handleEditShip saves an edit of a ship, kicking the commander first; the
// ship as it is now.
func (server *Server) handleEditShip(r *http.Request) (any, error) {
	commanderID, shipID, err := server.shipPath(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		TemplateID *uint32           `json:"template_id"`
		Level      *uint32           `json:"level"`
		Exp        *uint32           `json:"exp"`
		Intimacy   *uint32           `json:"intimacy"`
		Energy     *uint32           `json:"energy"`
		Locked     *bool             `json:"locked"`
		Propose    *bool             `json:"propose"`
		SkinID     *uint32           `json:"skin_id"`
		Retrofit   *bool             `json:"retrofit"`
		Fleet      *uint32           `json:"fleet"`
		Slot       uint32            `json:"slot"`
		Skills     map[uint32]uint32 `json:"skills"`
		Gear       map[uint32]uint32 `json:"gear"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	edit := game.ShipEdit{
		TemplateID: body.TemplateID, Level: body.Level, Exp: body.Exp, Intimacy: body.Intimacy, Energy: body.Energy,
		Locked: body.Locked, Propose: body.Propose, SkinID: body.SkinID, Retrofit: body.Retrofit, Fleet: body.Fleet,
		Slot: body.Slot, Skills: body.Skills, Gear: body.Gear,
	}
	if _, err := server.Game.EditShip(r.Context(), commanderID, shipID, edit); err != nil {
		return nil, err
	}
	return server.shipDetail(r, commanderID, shipID)
}

// strengthBody picks the attributes (ids 2 to 6) to max, all when empty.
type strengthBody struct {
	Attrs []uint32 `json:"attrs"`
}

// handleMaxShipStrength maxes a ship's attribute enhancement.
func (server *Server) handleMaxShipStrength(r *http.Request) (any, error) {
	commanderID, shipID, err := server.shipPath(r)
	if err != nil {
		return nil, err
	}
	var body strengthBody
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			return nil, err
		}
	}
	if _, err := server.Game.MaxShipStrength(r.Context(), commanderID, shipID, body.Attrs); err != nil {
		return nil, err
	}
	return server.shipDetail(r, commanderID, shipID)
}

// handleMaxDockStrength maxes the attribute enhancement of every ship.
func (server *Server) handleMaxDockStrength(r *http.Request) (any, error) {
	return commanderEdit(server, r, func(ctx context.Context, id uint32, body strengthBody) (bool, error) {
		return server.Game.MaxDockStrength(ctx, id, body.Attrs)
	})
}

// handleDockGear sets the gear of several ships at once.
func (server *Server) handleDockGear(r *http.Request) (any, error) {
	return commanderEdit(server, r, func(ctx context.Context, id uint32, body struct {
		Ships map[uint32]map[uint32]uint32 `json:"ships"`
	}) (bool, error) {
		return server.Game.EditDockGear(ctx, id, body.Ships)
	})
}

// handleRemoveShip takes a ship out of the dock.
func (server *Server) handleRemoveShip(r *http.Request) (any, error) {
	commanderID, shipID, err := server.shipPath(r)
	if err != nil {
		return nil, err
	}
	if _, err := server.Game.RemoveShip(r.Context(), commanderID, shipID); err != nil {
		return nil, err
	}
	return map[string]bool{"removed": true}, nil
}

type fleetShipRef struct {
	ID         uint32 `json:"id"`
	Name       string `json:"name"`
	Level      uint32 `json:"level"`
	Rarity     uint32 `json:"rarity"`
	Team       string `json:"team"`
	Type       string `json:"type"`
	TypeID     uint32 `json:"type_id"`
	Icon       string `json:"icon,omitempty"`
	SquareIcon string `json:"square_icon,omitempty"`
}

type fleetJSON struct {
	ID            uint32         `json:"id"`
	Name          string         `json:"name"`
	Locked        bool           `json:"locked"`
	UnlockChapter string         `json:"unlock_chapter"`
	UnlockMap     uint32         `json:"unlock_map"`
	Ships         []fleetShipRef `json:"ships"`
	Meowfficers   []uint32       `json:"meowfficers"`
	Oil           uint32         `json:"oil"`
	Duplicates    []uint32       `json:"duplicates"`
}

type teamJSON struct {
	Kind        string         `json:"kind"`
	Ships       []fleetShipRef `json:"ships"`
	Meowfficers []uint32       `json:"meowfficers"`
	Oil         uint32         `json:"oil"`
	Duplicates  []uint32       `json:"duplicates"`
	Problem     string         `json:"problem"`
	Limits      []string       `json:"limits"`
}

type eliteMaxJSON struct {
	Main      uint32 `json:"main"`
	Submarine uint32 `json:"submarine"`
	Support   uint32 `json:"support"`
}

type eliteJSON struct {
	Formation uint32       `json:"formation"`
	Chapters  []string     `json:"chapters"`
	Teams     []teamJSON   `json:"teams"`
	Max       eliteMaxJSON `json:"max"`
}

type activityFleetsJSON struct {
	ID     uint32      `json:"id"`
	Fleets []fleetJSON `json:"fleets"`
}

// handleFleets is a commander's formation, elite and activity fleets.
func (server *Server) handleFleets(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	tables, err := dockNames()
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	view, err := server.Game.Fleets(ctx, commander.ID)
	if err != nil {
		return nil, err
	}
	ships, err := server.Game.DockShips(ctx, commander.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint32]dockShipRow, len(ships))
	typeIDs := make(map[uint32]uint32, len(ships))
	for _, ship := range ships {
		byID[ship.ID] = server.dockShipRow(tables, ship)
		typeIDs[ship.ID] = ship.Type
	}
	refs := func(ids []uint32) []fleetShipRef {
		list := make([]fleetShipRef, 0, len(ids))
		for _, id := range ids {
			ship, ok := byID[id]
			if !ok {
				continue // no longer owned, as the client drops it
			}
			list = append(list, fleetShipRef{
				ID: id, Name: ship.Name, Level: ship.Level, Rarity: ship.Rarity, Team: ship.Team,
				Type: ship.Type, TypeID: typeIDs[id], Icon: ship.Icon, SquareIcon: ship.SquareIcon,
			})
		}
		return list
	}
	fleets := func(list []game.FleetView) []fleetJSON {
		out := make([]fleetJSON, 0, len(list))
		for _, fleet := range list {
			row := fleetJSON{
				ID: fleet.ID, Name: fleet.Name, Locked: fleet.Locked, Ships: refs(fleet.Ships), Meowfficers: fleet.Meowfficers,
				Oil: fleet.Oil, Duplicates: fleet.Duplicates,
			}
			if row.Duplicates == nil {
				row.Duplicates = []uint32{}
			}
			if chapter := fleet.UnlockChapter; chapter != 0 {
				// Fleet:isUnlock's tip names the map by the chapter id's first digit
				row.UnlockChapter = tables.chapters[chapter]
				if row.UnlockChapter == "" {
					row.UnlockChapter = fmt.Sprintf("%d-%d", chapter/100, chapter%100)
				}
				row.UnlockMap = uint32(strconv.FormatUint(uint64(chapter), 10)[0] - '0')
			}
			out = append(out, row)
		}
		return out
	}
	elite := make([]eliteJSON, 0, len(view.Elite))
	for _, saved := range view.Elite {
		row := eliteJSON{
			Formation: saved.Formation, Chapters: tables.formations[saved.Formation], Teams: []teamJSON{},
			Max: eliteMaxJSON{Main: saved.Max.Main, Submarine: saved.Max.Submarine, Support: saved.Max.Support},
		}
		for _, team := range saved.Teams {
			entry := teamJSON{
				Kind: team.Kind, Ships: refs(team.Ships), Meowfficers: team.Meowfficers,
				Oil: team.Oil, Duplicates: team.Duplicates, Problem: team.Problem, Limits: []string{},
			}
			if entry.Duplicates == nil {
				entry.Duplicates = []uint32{}
			}
			// a limitation slot is a hull, or a bundle of them
			for _, types := range team.Limits {
				names := make([]string, 0, len(types))
				for _, shipType := range types {
					// the quzhu bundle's two DDG types share a name
					if name := strings.TrimSpace(tables.shipTypes[shipType]); !slices.Contains(names, name) {
						names = append(names, name)
					}
				}
				entry.Limits = append(entry.Limits, strings.Join(names, " / "))
			}
			row.Teams = append(row.Teams, entry)
		}
		elite = append(elite, row)
	}
	activities := make([]activityFleetsJSON, 0, len(view.Activity))
	for _, activity := range view.Activity {
		activities = append(activities, activityFleetsJSON{ID: activity.Activity, Fleets: fleets(activity.Fleets)})
	}
	cats, err := server.Store.ListMeowfficers(ctx, commander.ID)
	if err != nil {
		return nil, err
	}
	catRefs := make([]namedRef, 0, len(cats))
	for _, cat := range cats {
		name := cat.Name
		if name == "" {
			name = tables.cats[cat.TemplateID].Name
		}
		catRefs = append(catRefs, namedRef{ID: cat.ID, Name: name, Rarity: tables.cats[cat.TemplateID].Rarity, Icon: tables.catIcon(cat.TemplateID)})
	}
	defense, set, err := server.Game.DefenseFleet(ctx, commander.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"fleets": fleets(view.Fleets), "elite": elite, "activity": activities, "meowfficers": catRefs,
		// the exercise defense fleet; not set is fleet 1's ships
		"defense": map[string]any{"ships": refs(defense), "set": set},
	}, nil
}

// handleEditFleet renames a formation fleet, sets its ships or its cats.
func (server *Server) handleEditFleet(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	fleetID, err := pathUint(r, "fleet")
	if err != nil {
		return nil, err
	}
	var body struct {
		Name        *string  `json:"name"`
		Ships       []uint32 `json:"ships"`
		Meowfficers []uint32 `json:"meowfficers"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	_, err = server.Game.SetFleet(r.Context(), commander.ID, fleetID, game.FleetEdit{Name: body.Name, Ships: body.Ships, Meowfficers: body.Meowfficers})
	if err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

func (server *Server) handleClearFleet(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	fleetID, err := pathUint(r, "fleet")
	if err != nil {
		return nil, err
	}
	if _, err := server.Game.ClearFleet(r.Context(), commander.ID, fleetID); err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

func (server *Server) handleClearEliteFleet(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	formation, err := pathUint(r, "formation")
	if err != nil {
		return nil, err
	}
	if _, err := server.Game.ClearEliteFleet(r.Context(), commander.ID, formation); err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

// fleetBody is the ships (in order) and cats ([command, support]) given to
// an elite team or an activity fleet, nil kept.
type fleetBody struct {
	Ships       []uint32 `json:"ships"`
	Meowfficers []uint32 `json:"meowfficers"`
}

// handleEditEliteTeam sets the ships and cats of a team (kind main,
// submarine or support, index 1-based within its kind) of a formation's
// saved elite fleets.
func (server *Server) handleEditEliteTeam(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	formation, err := pathUint(r, "formation")
	if err != nil {
		return nil, err
	}
	index, err := pathUint(r, "index")
	if err != nil {
		return nil, err
	}
	var body fleetBody
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	_, err = server.Game.SetEliteTeam(r.Context(), commander.ID, formation, r.PathValue("kind"), index, body.Ships, body.Meowfficers)
	if err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

// handleEditActivityFleet sets the ships and cats of one of an activity's
// own fleets.
func (server *Server) handleEditActivityFleet(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	activity, err := pathUint(r, "activity")
	if err != nil {
		return nil, err
	}
	fleetID, err := pathUint(r, "fleet")
	if err != nil {
		return nil, err
	}
	var body fleetBody
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	if _, err := server.Game.SetActivityFleet(r.Context(), commander.ID, activity, fleetID, body.Ships, body.Meowfficers); err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

func (server *Server) handleClearActivityFleet(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	activity, err := pathUint(r, "activity")
	if err != nil {
		return nil, err
	}
	if _, err := server.Game.ClearActivityFleet(r.Context(), commander.ID, activity); err != nil {
		return nil, err
	}
	return server.handleFleets(r)
}

type meowfficerJSON struct {
	ID           uint32   `json:"id"`
	TemplateID   uint32   `json:"template_id"`
	Name         string   `json:"name"`
	TemplateName string   `json:"template_name"`
	Icon         string   `json:"icon,omitempty"`
	Rarity       uint32   `json:"rarity"`
	Nation       string   `json:"nation"`
	Level        uint32   `json:"level"`
	MaxLevel     uint32   `json:"max_level"`
	Exp          uint32   `json:"exp"`
	ExpNext      uint32   `json:"exp_next"`
	Locked       bool     `json:"locked"`
	Fleet        uint32   `json:"fleet"`
	Cattery      uint32   `json:"cattery"`
	Skill        namedRef `json:"skill"`
	// the skill's EXP, toward skill_exp_next (0 at skill_max_level)
	SkillExp      uint32 `json:"skill_exp"`
	SkillExpNext  uint32 `json:"skill_exp_next"`
	SkillMaxLevel uint32 `json:"skill_max_level"`
	// the EXP each level takes, from Lv.1: the cat's, its skill's
	LevelExps      []uint32   `json:"level_exps"`
	SkillLevelExps []uint32   `json:"skill_level_exps"`
	Talents        []namedRef `json:"talents"`
}

type meowfficerBoxJSON struct {
	ID         uint32   `json:"id"`
	Box        namedRef `json:"box"`
	Holds      string   `json:"holds"`
	BeginTime  int64    `json:"begin_time"`
	FinishTime int64    `json:"finish_time"`
	Ready      bool     `json:"ready"`
}

type catterySlotJSON struct {
	ID         uint32 `json:"id"`
	Meowfficer uint32 `json:"meowfficer"`
	Style      uint32 `json:"style"`
	Cleaned    bool   `json:"cleaned"`
	Fed        bool   `json:"fed"`
	Played     bool   `json:"played"`
}

type presetJSON struct {
	ID          uint32   `json:"id"`
	Name        string   `json:"name"`
	Meowfficers []uint32 `json:"meowfficers"`
}

func (tables *dockNameTables) catIcon(templateID uint32) string {
	if painting := tables.cats[templateID].Painting; painting != "" {
		return "commandericon/" + strings.ToLower(painting)
	}
	return ""
}

// handleMeowfficers is a commander's cats, training boxes, cattery and
// saved pairs, and the cats that can be granted.
func (server *Server) handleMeowfficers(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	tables, err := dockNames()
	if err != nil {
		return nil, err
	}
	view, err := server.Game.Meowfficers(r.Context(), commander.ID)
	if err != nil {
		return nil, err
	}
	cats := make([]meowfficerJSON, 0, len(view.Meowfficers))
	for _, cat := range view.Meowfficers {
		template := tables.cats[cat.TemplateID]
		row := meowfficerJSON{
			ID: cat.ID, TemplateID: cat.TemplateID, Name: cat.Name, TemplateName: template.Name, Icon: tables.catIcon(cat.TemplateID),
			Rarity: cat.Rarity, Nation: tables.nations[cat.Nationality], Level: cat.Level, MaxLevel: cat.MaxLevel, Exp: cat.Exp,
			ExpNext: cat.ExpNext, Locked: cat.Locked, Fleet: cat.Fleet, Cattery: cat.Cattery, Talents: []namedRef{},
		}
		if row.Name == "" {
			row.Name = template.Name
		}
		skill := tables.catSkills[cat.SkillID]
		row.Skill = namedRef{ID: cat.SkillID, Name: skill.Name, Count: cat.SkillLevel}
		row.SkillExp, row.SkillExpNext, row.SkillMaxLevel = cat.SkillExp, cat.SkillExpNext, cat.SkillMaxLevel
		row.LevelExps, row.SkillLevelExps = cat.LevelExps, cat.SkillLevelExps
		for _, id := range cat.Talents {
			row.Talents = append(row.Talents, namedRef{ID: id, Name: tables.talents[id]})
		}
		cats = append(cats, row)
	}
	boxes := make([]meowfficerBoxJSON, 0, len(view.Boxes))
	for _, box := range view.Boxes {
		item := tables.boxItems[box.PoolID]
		named := server.names[dropItem][item]
		boxes = append(boxes, meowfficerBoxJSON{
			ID: box.BoxID, Box: namedRef{ID: item, Name: named.Name, Rarity: named.Rarity, Icon: named.Icon}, Holds: tables.cats[box.TemplateID].Name,
			BeginTime: box.BeginTime, FinishTime: box.FinishTime, Ready: box.Ready,
		})
	}
	slots := make([]catterySlotJSON, 0, len(view.Slots))
	for _, slot := range view.Slots {
		slots = append(slots, catterySlotJSON{ID: slot.ID, Meowfficer: slot.MeowfficerID, Style: slot.Style, Cleaned: slot.Cleaned, Fed: slot.Fed, Played: slot.Played})
	}
	presets := make([]presetJSON, 0, len(view.Presets))
	for _, preset := range view.Presets {
		presets = append(presets, presetJSON{ID: preset.ID, Name: preset.Name, Meowfficers: preset.Meowfficers})
	}
	templates := make([]namedRef, 0, len(view.Templates))
	for _, id := range view.Templates {
		template := tables.cats[id]
		templates = append(templates, namedRef{ID: id, Name: template.Name, Rarity: template.Rarity, Icon: tables.catIcon(id)})
	}
	sort.SliceStable(templates, func(i, j int) bool { return templates[i].Name < templates[j].Name })
	return map[string]any{
		"meowfficers": cats, "capacity": view.Capacity, "boxes": boxes, "box_count": view.BoxCount,
		"bought_today": view.BoughtToday, "bought_total": view.BoughtTotal,
		"cattery": map[string]any{
			"level": view.CatteryLevel, "exp": view.CatteryExp, "exp_next": view.CatteryNext, "clean": view.Clean, "clean_max": view.CleanMax, "slots": slots,
		},
		"presets": presets, "templates": templates,
	}, nil
}

func (server *Server) handleGrantMeowfficer(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		TemplateID uint32 `json:"template_id"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	if _, err := server.Game.GrantMeowfficer(r.Context(), commander.ID, body.TemplateID); err != nil {
		return nil, err
	}
	return server.handleMeowfficers(r)
}

func (server *Server) handleEditMeowfficer(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	catID, err := pathUint(r, "cat")
	if err != nil {
		return nil, err
	}
	var body struct {
		Name   *string `json:"name"`
		Level  *uint32 `json:"level"`
		Exp    *uint32 `json:"exp"`
		Locked *bool   `json:"locked"`
		// the skill's level and EXP
		SkillLevel *uint32 `json:"skill_level"`
		SkillExp   *uint32 `json:"skill_exp"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	edit := game.MeowfficerEdit{Name: body.Name, Level: body.Level, Exp: body.Exp, Locked: body.Locked, SkillLevel: body.SkillLevel, SkillExp: body.SkillExp}
	if _, err := server.Game.EditMeowfficer(r.Context(), commander.ID, catID, edit); err != nil {
		return nil, err
	}
	return server.handleMeowfficers(r)
}

// meowfficerAction runs a cattery action on the {id} commander with the
// {name} path value, answering the cats as they are after.
func (server *Server) meowfficerAction(r *http.Request, name string, action func(commanderID, value uint32) (bool, error)) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	value := uint32(0)
	if name != "" {
		if value, err = pathUint(r, name); err != nil {
			return nil, err
		}
	}
	if _, err := action(commander.ID, value); err != nil {
		return nil, err
	}
	return server.handleMeowfficers(r)
}

func (server *Server) handleFinishMeowfficerBox(r *http.Request) (any, error) {
	return server.meowfficerAction(r, "box", func(commanderID, box uint32) (bool, error) {
		return server.Game.FinishMeowfficerBox(r.Context(), commanderID, box)
	})
}

func (server *Server) handleEmptyMeowfficerBox(r *http.Request) (any, error) {
	return server.meowfficerAction(r, "box", func(commanderID, box uint32) (bool, error) {
		return server.Game.EmptyMeowfficerBox(r.Context(), commanderID, box)
	})
}

func (server *Server) handleFillCatteryClean(r *http.Request) (any, error) {
	return server.meowfficerAction(r, "", func(commanderID, _ uint32) (bool, error) {
		return server.Game.FillCatteryClean(r.Context(), commanderID)
	})
}

func (server *Server) handleResetCatteryOps(r *http.Request) (any, error) {
	return server.meowfficerAction(r, "", func(commanderID, _ uint32) (bool, error) {
		return server.Game.ResetCatteryOps(r.Context(), commanderID)
	})
}

func (server *Server) handleClearMeowfficerPreset(r *http.Request) (any, error) {
	return server.meowfficerAction(r, "preset", func(commanderID, preset uint32) (bool, error) {
		return server.Game.ClearMeowfficerPreset(r.Context(), commanderID, preset)
	})
}
