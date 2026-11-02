package game

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// The back-office's dock, fleets and meowfficers (internal/admin): what a
// commander's ships, fleets and cats are, and the edits it makes to them.
// Every edit kicks the commander first (the client keeps its own copy) and
// runs in one transaction, through the same helpers the requests use.

// Fleet ids (model/vo/fleet.lua, FleetProxy): the surface fleets 1-6, the
// submarine fleets 11-14 (SUBMARINE_FLEET_NUMS), the exercise fleet.
const (
	fleetSurfaceLast   = 6
	fleetSubmarine     = 11
	fleetSubmarineLast = 14
	fleetExercise      = 101 // FleetProxy.PVP_FLEET_ID
)

// regularFleet is Fleet:isRegularFleet, the fleets a ship is in at most one
// of.
func regularFleet(id uint32) bool {
	return id >= fleetRegular && id <= fleetSurfaceLast || id >= fleetSubmarine && id <= fleetSubmarineLast
}

// fleetUnlockChapters are the chapters Fleet:isUnlock waits for, fleets 3
// to 6.
var fleetUnlockChapters = map[uint32]uint32{3: 404, 4: 504, 5: 604, 6: 704}

// chapterCleared is Chapter:isClear, progress 100.
const chapterCleared = 100

// shipTeamOf is Ship:getTeamType from the ship's current template.
func shipTeamOf(ship db.Ship) string {
	if template, ok := ownableShips[ship.TemplateID]; ok {
		return shipTeams[template.Type]
	}
	return shipTeam(ship)
}

// starChain is the ship_data_breakout chain the template is on, first star
// first: the templates a limit break moves a ship through.
func starChain(templateID uint32) []uint32 {
	prev := func(id uint32) (uint32, bool) {
		for from, row := range breakouts {
			if row.BreakoutID == id {
				return from, true
			}
		}
		return 0, false
	}
	chain := []uint32{templateID}
	for id, ok := prev(templateID); ok && len(chain) < 16; id, ok = prev(id) {
		chain = slices.Insert(chain, 0, id)
	}
	for next := breakouts[templateID].BreakoutID; next != 0 && len(chain) < 16; next = breakouts[next].BreakoutID {
		if _, ok := ownableShips[next]; !ok {
			break
		}
		chain = append(chain, next)
	}
	return chain
}

// DockShip is one of a commander's ships as the dock lists it.
type DockShip struct {
	ID          uint32
	TemplateID  uint32
	Name        string // its own name, "" for its template's
	SkinID      uint32 // the skin it wears, its default one when it wears none
	Star        uint32
	MaxStar     uint32
	Level       uint32
	MaxLevel    uint32
	Rarity      uint32
	Type        uint32
	Nationality uint32
	Locked      bool
	Married     bool
	Fleet       uint32 // the regular fleet it is in, 0 none
	Team        string // vanguard, main or submarine
	CreatedAt   int64
}

func dockShip(ship db.Ship, fleets map[uint32]uint32) DockShip {
	template := ownableShips[ship.TemplateID]
	row := DockShip{
		ID: ship.ID, TemplateID: ship.TemplateID, Name: ship.Name, SkinID: ship.SkinID,
		Star: template.Star, Level: ship.Level, MaxLevel: ship.MaxLevel,
		Rarity: template.Rarity, Type: template.Type, Nationality: template.Nationality,
		Locked: ship.IsLocked, Married: ship.Propose != 0, Fleet: fleets[ship.ID], Team: shipTeamOf(ship),
		CreatedAt: ship.CreatedAt,
	}
	if row.SkinID == 0 {
		row.SkinID = template.defaultSkin
	}
	chain := starChain(ship.TemplateID)
	row.MaxStar = max(template.Star, ownableShips[chain[len(chain)-1]].Star)
	return row
}

// regularFleets are the commander's ships by the regular fleet they're in.
func regularFleets(ctx context.Context, queries *db.Queries, commanderID uint32) (map[uint32]uint32, error) {
	rows, err := queries.ListFleetShips(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	fleets := map[uint32]uint32{}
	for _, row := range rows {
		if regularFleet(row.FleetID) {
			fleets[row.ShipID] = row.FleetID
		}
	}
	return fleets, nil
}

// DockShips lists a commander's ships.
func (game *Game) DockShips(ctx context.Context, commanderID uint32) ([]DockShip, error) {
	queries := game.DB.Queries
	ships, err := queries.ListShipsByCommander(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	fleets, err := regularFleets(ctx, queries, commanderID)
	if err != nil {
		return nil, err
	}
	list := make([]DockShip, len(ships))
	for i, ship := range ships {
		list[i] = dockShip(ship, fleets)
	}
	return list, nil
}

// ShipStar is one limit break a ship can be set to.
type ShipStar struct {
	TemplateID uint32
	Star       uint32
	MaxLevel   uint32
}

// ShipSkillLevel is a skill a ship learnt.
type ShipSkillLevel struct {
	ID    uint32
	Level uint32
	Max   uint32
	Type  uint32 // skill_data_template type: 1 offense, 2 defense, 3 support
}

// ShipGearSlot is one of a ship's 5 slots: what it holds and the depot's
// equipment it can take.
type ShipGearSlot struct {
	Pos     uint32
	EquipID uint32
	SkinID  uint32
	Fits    []uint32
}

// ShipStat is one of Ship.PROPERTIES as Ship:getShipProperties computes it.
type ShipStat struct {
	Name  string
	Value float64
}

// ShipRetrofit is a ship's retrofit tree: its nodes done and how many, and
// whether every node is.
type ShipRetrofit struct {
	Nodes []uint32
	Done  int
	Full  bool
	// what the full retrofit needs, the highest level_limit and star_limit
	// of the tree
	Level, Star uint32
}

// ShipFleet is a regular fleet a ship can be put in.
type ShipFleet struct {
	ID     uint32
	Locked bool
}

// ShipDetail is one ship with what can be edited of it.
type ShipDetail struct {
	DockShip
	Exp     uint32
	ExpNext uint32 // the exp from its level to the next, 0 at its max level
	ExpMax  uint32 // what exp can be set to
	// ExpMaxes is ExpMax at each level from 1 to LevelCap, as EditShip
	// checks it once the level is set (its template kept)
	ExpMaxes    []uint32
	LevelCap    uint32 // the highest level it can be set to (Cognitive Awakening)
	Intimacy    uint32
	IntimacyMax uint32
	Energy      uint32
	EnergyMax   uint32
	Propose     uint32
	Stars       []ShipStar
	Skins       []uint32 // the skins it can wear, its default one first
	Retrofit    *ShipRetrofit
	Fleets      []ShipFleet
	Slot        uint32 // its place (1-3) in its fleet's team
	Skills      []ShipSkillLevel
	Gear        []ShipGearSlot
	Stats       []ShipStat
	// what keeps it from being edited or removed
	InChapter bool
}

// commanderShip loads one of the commander's ships, an AdminError when it
// isn't theirs.
func adminShip(ctx context.Context, queries *db.Queries, commanderID, shipID uint32) (db.Ship, error) {
	ship, err := queries.GetShip(ctx, db.GetShipParams{ID: shipID, CommanderID: commanderID})
	if errors.Is(err, sql.ErrNoRows) {
		return ship, adminMissing("commander %d has no ship %d", commanderID, shipID)
	}
	return ship, err
}

// ShipDetail is one of the commander's ships with what can be edited of it.
func (game *Game) ShipDetail(ctx context.Context, commanderID, shipID uint32) (ShipDetail, error) {
	tx := newTx(ctx, game.DB.Queries, commanderID)
	ship, err := adminShip(ctx, tx.Queries, commanderID, shipID)
	if err != nil {
		return ShipDetail{}, err
	}
	fleets, err := regularFleets(ctx, tx.Queries, commanderID)
	if err != nil {
		return ShipDetail{}, err
	}
	template := ownableShips[ship.TemplateID]
	detail := ShipDetail{
		DockShip: dockShip(ship, fleets), Exp: ship.Exp, Intimacy: ship.Intimacy, Energy: ship.Energy, Propose: ship.Propose,
		IntimacyMax: maxIntimacy, EnergyMax: energyMax(),
	}
	if ship.Propose != 0 {
		detail.IntimacyMax = 2 * maxIntimacy
	}
	curve := shipLevelCurve(ship.TemplateID)
	if ship.Level < ship.MaxLevel && int(ship.Level) <= len(curve) {
		detail.ExpNext = curve[ship.Level-1]
		detail.ExpMax = detail.ExpNext - 1
	} else if shipLevelings[ship.TemplateID].accumulate {
		detail.ExpMax = expOverflowMax
	}
	detail.LevelCap = levelCap(ship.TemplateID, ship.MaxLevel)
	for level := uint32(1); level <= detail.LevelCap; level++ {
		maxLevel := awakenedMaxLevel(ship.TemplateID, level)
		if ship.MaxLevel > maxLevel && shipLevelings[ship.TemplateID].accumulate {
			maxLevel = min(ship.MaxLevel, shipConfigMaxLevel)
		}
		expMax := uint32(0)
		if level < maxLevel && int(level) <= len(curve) {
			expMax = curve[level-1] - 1
		} else if shipLevelings[ship.TemplateID].accumulate {
			expMax = expOverflowMax
		}
		detail.ExpMaxes = append(detail.ExpMaxes, expMax)
	}
	for _, id := range starChain(ship.TemplateID) {
		next := ownableShips[id]
		detail.Stars = append(detail.Stars, ShipStar{TemplateID: id, Star: next.Star, MaxLevel: min(shipConfigMaxLevel, next.MaxLevel)})
	}

	// skins: its default, then those of its group it can put on
	detail.Skins = []uint32{template.defaultSkin}
	group := shipGroup(ship.TemplateID)
	now := time.Now()
	for _, id := range sortedKeys(shipSkins) {
		skin := shipSkins[id]
		if id == template.defaultSkin || skin.group != group && !slices.Contains(shareGroups[group], skin.group) {
			continue
		}
		if id == ship.SkinID || tx.canWear(ship, id, now) == nil {
			detail.Skins = append(detail.Skins, id)
		}
	}

	transformRows, err := tx.ListShipTransforms(ctx, ship.ID)
	if err != nil {
		return detail, err
	}
	if nodes := transformGroups[group]; len(nodes) > 0 {
		retrofit := &ShipRetrofit{Nodes: nodes}
		levels := transformLevels(transformRows)
		for _, id := range nodes {
			if levels[id] > 0 {
				retrofit.Done++
			}
			retrofit.Level = max(retrofit.Level, transforms[id].LevelLimit)
			retrofit.Star = max(retrofit.Star, transforms[id].StarLimit)
		}
		retrofit.Full = retrofitDone(group, levels)
		detail.Retrofit = retrofit
	}

	// fleets
	rows, err := tx.ListFleetShips(ctx, commanderID)
	if err != nil {
		return detail, err
	}
	if detail.Fleet != 0 {
		slot := uint32(0)
		for _, row := range rows {
			if row.FleetID != detail.Fleet {
				continue
			}
			member, err := tx.GetShip(ctx, db.GetShipParams{ID: row.ShipID, CommanderID: commanderID})
			if err != nil {
				return detail, err
			}
			if shipTeamOf(member) == detail.Team {
				slot++
			}
			if row.ShipID == ship.ID {
				detail.Slot = slot
				break
			}
		}
	}
	if detail.Fleets, err = game.shipFleets(ctx, tx.Queries, commanderID, detail.Team); err != nil {
		return detail, err
	}

	learnt, err := tx.ListShipSkillsByID(ctx, db.ListShipSkillsByIDParams{CommanderID: commanderID, Ids: []uint32{ship.ID}})
	if err != nil {
		return detail, err
	}
	for _, skill := range learnt {
		detail.Skills = append(detail.Skills, ShipSkillLevel{ID: skill.SkillID, Level: skill.Level, Max: skillMaxLevel(skill.SkillID), Type: skills[skill.SkillID].Type})
	}

	slots, err := shipEquipments(ctx, tx.Queries, commanderID, ship.ID)
	if err != nil {
		return detail, err
	}
	depot, err := tx.ListEquipments(ctx, commanderID)
	if err != nil {
		return detail, err
	}
	for _, slot := range slots {
		gear := ShipGearSlot{Pos: slot.Pos, EquipID: slot.EquipID, SkinID: slot.SkinID, Fits: []uint32{}}
		for _, row := range depot {
			if row.Count > 0 && fitsSlot(ship.TemplateID, row.EquipID, slot.Pos) {
				gear.Fits = append(gear.Fits, row.EquipID)
			}
		}
		detail.Gear = append(detail.Gear, gear)
	}

	if detail.Stats, err = shipStats(ctx, tx.Queries, ship, transformRows); err != nil {
		return detail, err
	}
	inChapter, err := shipsInChapter(ctx, tx.Queries, commanderID)
	if err != nil {
		return detail, err
	}
	detail.InChapter = inChapter[ship.ID]
	return detail, nil
}

// energyMax is the last energy_template bracket's upper bound, 150.
func energyMax() uint32 {
	top := uint32(0)
	for _, row := range energyStates {
		top = max(top, row.UpperBound)
	}
	return top
}

// shipLevelCurve is the exp each level takes a ship of the template, the UR
// curve for the UR ships (addShipExp).
func shipLevelCurve(templateID uint32) []uint32 {
	if shipLevelings[templateID].ur {
		return shipLevelExpUR
	}
	return shipLevelExp
}

// levelCap is the highest level a ship of templateID can be set to: its
// template's max level, or for a ship Cognitive Awakening raises
// (can_get_proficency, Ship:canUpgradeMaxLevel) the last ship_level, and
// never below the max level it reached.
func levelCap(templateID, maxLevel uint32) uint32 {
	top := min(shipConfigMaxLevel, ownableShips[templateID].MaxLevel)
	if shipLevelings[templateID].accumulate && breakouts[templateID].BreakoutID == 0 {
		if _, ok := nextMaxLevel(db.Ship{MaxLevel: top}); ok {
			top = shipConfigMaxLevel
		}
	}
	return max(top, min(maxLevel, shipConfigMaxLevel))
}

// awakenedMaxLevel is the max level a ship at level needs: its template's,
// or the first ship_level level_limit at or above level past it.
func awakenedMaxLevel(templateID, level uint32) uint32 {
	top := min(shipConfigMaxLevel, ownableShips[templateID].MaxLevel)
	for top < level {
		next, ok := nextMaxLevel(db.Ship{MaxLevel: top})
		if !ok {
			break
		}
		top = next
	}
	return top
}

// retrofitDone tells whether every node of a group's retrofit tree is done:
// at its max level, or removed by a done node's edit_trans.
func retrofitDone(group uint32, levels map[uint32]uint32) bool {
	removed := map[uint32]bool{}
	for id, level := range levels {
		if level > 0 {
			for _, gone := range transforms[id].EditTrans {
				removed[gone] = true
			}
		}
	}
	for _, id := range transformGroups[group] {
		if !removed[id] && levels[id] < transforms[id].MaxLevel {
			return false
		}
	}
	return true
}

// shipFleets are the regular fleets a ship of team can be put in: the
// surface or the submarine ones, locked while Fleet:isUnlock's chapter isn't
// cleared; 13 and 14 only once they exist.
func (game *Game) shipFleets(ctx context.Context, queries *db.Queries, commanderID uint32, team string) ([]ShipFleet, error) {
	existing, err := queries.ListFleets(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	exists := map[uint32]bool{}
	for _, row := range existing {
		exists[row.FleetID] = true
	}
	var list []ShipFleet
	if team == teamSubmarine {
		for id := uint32(fleetSubmarine); id <= fleetSubmarineLast; id++ {
			if id <= fleetSubmarine+1 || exists[id] {
				list = append(list, ShipFleet{ID: id})
			}
		}
		return list, nil
	}
	for id := uint32(fleetRegular); id <= fleetSurfaceLast; id++ {
		locked, err := fleetLocked(ctx, queries, commanderID, id)
		if err != nil {
			return nil, err
		}
		list = append(list, ShipFleet{ID: id, Locked: locked && !exists[id]})
	}
	return list, nil
}

// fleetLocked is Fleet:isUnlock: fleets 3 to 6 wait for their chapter.
func fleetLocked(ctx context.Context, queries *db.Queries, commanderID, fleetID uint32) (bool, error) {
	chapter, ok := fleetUnlockChapters[fleetID]
	if !ok {
		return false, nil
	}
	progress, err := queries.GetChapterProgress(ctx, db.GetChapterProgressParams{CommanderID: commanderID, ChapterID: chapter})
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return progress.Progress < chapterCleared, err
}

// shipProperties is Ship.PROPERTIES (model/vo/ship.lua) by index, armor
// (a class, not a value) left out.
var shipProperties = []string{"durability", "cannon", "torpedo", "antiaircraft", "air", "reload", "armor", "hit", "dodge", "speed", "luck", "antisub"}

// intimacyStats are the properties Ship:intimacyAdditions raises.
var intimacyStats = map[string]bool{"durability": true, "cannon": true, "torpedo": true, "antiaircraft": true, "antisub": true, "air": true, "reload": true, "hit": true, "dodge": true}

// shipStats is Ship:getShipProperties for a regular ship: its base and
// growth at its level, its enhancement, affinity and retrofits; no gear,
// meowfficers or tech. Research and META ships add what belfast doesn't
// model, so they have none.
func shipStats(ctx context.Context, queries *db.Queries, ship db.Ship, transformRows []db.ShipTransform) ([]ShipStat, error) {
	template, ok := ownableShips[ship.TemplateID]
	if !ok || template.unretirable {
		return nil, nil
	}
	row, ok := shipStatistics[ship.TemplateID]
	if !ok {
		return nil, nil
	}
	values := map[string]float64{}
	for i, name := range shipProperties {
		if i >= len(row.Attrs) || i >= len(row.AttrsGrowth) {
			continue
		}
		value := row.Attrs[i] + (float64(ship.Level)-1)*row.AttrsGrowth[i]/1000
		if ship.Level > extraAttrLevelLimit && i < len(row.AttrsGrowthExtra) {
			value += float64(ship.Level-extraAttrLevelLimit) * row.AttrsGrowthExtra[i] / 1000
		}
		values[name] = value
	}
	strengths, err := queries.ListShipStrengths(ctx, ship.ID)
	if err != nil {
		return nil, err
	}
	strengthen := strengthens[template.StrengthenID]
	for _, strength := range strengths {
		for i, attr := range shipModAttrs {
			if attr.id != strength.AttrID || i >= len(strengthen.Durability) || i >= len(strengthen.LevelExp) {
				continue
			}
			capped := min(strength.Exp, strengthen.Durability[i]*strengthen.LevelExp[i])
			values[attr.name] += calcFloor(float64(capped) / float64(max(shipModExpRatio(ship, strengthen, i), 1)))
		}
	}
	bonus := float64(intimacyBonus(ship.Intimacy, ship.Propose != 0)) / 10000
	for name := range values {
		if intimacyStats[name] {
			values[name] *= 1 + bonus
		}
	}
	for name := range values {
		values[name] += transformBonus(transformRows, name)
	}
	stats := make([]ShipStat, 0, len(shipProperties))
	for _, name := range shipProperties {
		if value, ok := values[name]; ok && name != "armor" {
			stats = append(stats, ShipStat{Name: name, Value: calcFloor(value)})
		}
	}
	return stats, nil
}

// ShipEdit is what the back-office changes of a ship, nil fields kept.
type ShipEdit struct {
	TemplateID *uint32 // a limit break: a template of its starChain
	Level      *uint32
	Exp        *uint32
	Intimacy   *uint32
	Energy     *uint32
	Locked     *bool
	Propose    *bool
	SkinID     *uint32 // 0 for its default skin
	Retrofit   *bool   // true does every node of its tree, false undoes them
	Fleet      *uint32 // a regular fleet, 0 out of its fleet
	Slot       uint32  // its place (1-3) in the fleet's team, 0 last
	Skills     map[uint32]uint32
	Gear       map[uint32]uint32 // slot: equipment from the depot, 0 empty
}

// EditShip applies an edit to one of the commander's ships.
func (game *Game) EditShip(ctx context.Context, commanderID, shipID uint32, edit ShipEdit) (bool, error) {
	if _, err := adminShip(ctx, game.DB.Queries, commanderID, shipID); err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		ship, err := adminShip(ctx, tx.Queries, commanderID, shipID)
		if err != nil {
			return err
		}
		inChapter, err := shipsInChapter(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		if inChapter[ship.ID] {
			return adminRefused("the ship is in the chapter being played")
		}
		return tx.editShip(ship, edit)
	})
}

func (tx *Tx) editShip(ship db.Ship, edit ShipEdit) error {
	ctx := tx.ctx
	before := ship
	if edit.Retrofit != nil && !*edit.Retrofit {
		if err := tx.undoRetrofit(&ship); err != nil {
			return err
		}
	}
	if edit.TemplateID != nil && *edit.TemplateID != ship.TemplateID {
		chain := starChain(ship.TemplateID)
		if !slices.Contains(chain, *edit.TemplateID) {
			return adminRefused("template %d isn't a limit break of this ship", *edit.TemplateID)
		}
		if err := tx.setShipStar(&ship, *edit.TemplateID); err != nil {
			return err
		}
	}
	if edit.Level != nil {
		level := *edit.Level
		if level < 1 || level > levelCap(ship.TemplateID, ship.MaxLevel) {
			return adminRefused("level %d is past what the ship can reach (%d)", level, levelCap(ship.TemplateID, ship.MaxLevel))
		}
		if level != ship.Level {
			ship.Level = level
			ship.Exp = 0
		}
	}
	// the max level the level needs, a Cognitive Awakening kept
	ship.MaxLevel = awakenedMaxLevel(ship.TemplateID, ship.Level)
	if before.TemplateID == ship.TemplateID && before.MaxLevel > ship.MaxLevel && shipLevelings[ship.TemplateID].accumulate {
		ship.MaxLevel = min(before.MaxLevel, shipConfigMaxLevel)
	}
	if edit.Exp != nil {
		ship.Exp = *edit.Exp
	}
	curve := shipLevelCurve(ship.TemplateID)
	switch {
	case ship.Level < ship.MaxLevel && int(ship.Level) <= len(curve):
		if ship.Exp >= curve[ship.Level-1] {
			return adminRefused("exp %d is a level's worth: Lv.%d takes %d", ship.Exp, ship.Level, curve[ship.Level-1])
		}
	case shipLevelings[ship.TemplateID].accumulate:
		if ship.Exp > expOverflowMax {
			return adminRefused("exp past the max level stops at %d", expOverflowMax)
		}
	default:
		ship.Exp = 0
	}

	if edit.Propose != nil {
		switch {
		case *edit.Propose && ship.Propose == 0:
			intimacy := ship.Intimacy
			if edit.Intimacy != nil {
				intimacy = *edit.Intimacy
			}
			if intimacy < maxIntimacy {
				return adminRefused("the oath takes 100 affinity")
			}
			ship.Propose = uint32(time.Now().Unix())
			ship.IsLocked = true
			if err := tx.SetMarryShip(ctx, db.SetMarryShipParams{MarryShip: ship.ID, ID: tx.commanderID}); err != nil {
				return err
			}
		case !*edit.Propose && ship.Propose != 0:
			ship.Propose = 0
			ship.Intimacy = min(ship.Intimacy, maxIntimacy)
			if err := tx.AdminDockClearMarryShip(ctx, db.AdminDockClearMarryShipParams{ID: tx.commanderID, MarryShip: ship.ID}); err != nil {
				return err
			}
		}
	}
	if edit.Intimacy != nil {
		top := uint32(maxIntimacy)
		if ship.Propose != 0 {
			top = 2 * maxIntimacy
		}
		if *edit.Intimacy > top {
			return adminRefused("affinity stops at %d", top/100)
		}
		ship.Intimacy = *edit.Intimacy
	}
	if edit.Energy != nil {
		if *edit.Energy > energyMax() {
			return adminRefused("morale stops at %d", energyMax())
		}
		ship.Energy = *edit.Energy
	}
	if edit.Locked != nil {
		if ship.Propose != 0 && !*edit.Locked {
			return adminRefused("an oathed ship stays locked")
		}
		ship.IsLocked = *edit.Locked
	}
	if ship.Propose != 0 {
		ship.IsLocked = true
	}
	if err := tx.AdminDockSetShip(ctx, db.AdminDockSetShipParams{
		TemplateID: ship.TemplateID, Level: ship.Level, Exp: ship.Exp, MaxLevel: ship.MaxLevel, Intimacy: ship.Intimacy,
		Energy: ship.Energy, IsLocked: ship.IsLocked, Propose: ship.Propose, ID: ship.ID, CommanderID: tx.commanderID,
	}); err != nil {
		return err
	}
	if raised, err := raiseShipGroup(ctx, tx.Queries, ship); err != nil {
		return err
	} else if raised {
		tx.techPointsChanged()
	}

	if edit.Retrofit != nil && *edit.Retrofit {
		if err := tx.doRetrofit(&ship); err != nil {
			return err
		}
	}
	if err := checkRetrofitLimits(ctx, tx.Queries, ship); err != nil {
		return err
	}

	// the skin, checked again when the oath or retrofit changed
	skin := ship.SkinID
	if edit.SkinID != nil {
		skin = *edit.SkinID
	}
	if err := tx.setShipSkin(ship.ID, skin, 0, time.Now()); refused(err) {
		return adminRefused("the ship can't wear skin %d", skin)
	} else if err != nil {
		return err
	}

	for skillID, level := range edit.Skills {
		row, err := tx.GetShipSkill(ctx, db.GetShipSkillParams{CommanderID: tx.commanderID, ShipID: ship.ID, SkillID: skillID})
		if errors.Is(err, sql.ErrNoRows) {
			return adminRefused("the ship hasn't skill %d", skillID)
		} else if err != nil {
			return err
		}
		if level < 1 || level > skillMaxLevel(skillID) {
			return adminRefused("skill %d goes from level 1 to %d", skillID, skillMaxLevel(skillID))
		}
		if level != row.Level {
			if err := tx.SetShipSkillExp(ctx, db.SetShipSkillExpParams{Level: level, Exp: 0, ShipID: ship.ID, SkillID: skillID}); err != nil {
				return err
			}
		}
	}

	if err := tx.editShipGear(ship, edit.Gear); err != nil {
		return err
	}
	if edit.Fleet != nil {
		if err := tx.moveShipToFleet(ship, *edit.Fleet, edit.Slot); err != nil {
			return err
		}
	} else if shipTeamOf(ship) != shipTeamOf(before) {
		fleets, err := regularFleets(ctx, tx.Queries, tx.commanderID)
		if err != nil {
			return err
		}
		if fleets[ship.ID] != 0 {
			return adminRefused("the retrofit changes the ship's team: take it out of fleet %d first", fleets[ship.ID])
		}
	}
	return setShipTrophies(ctx, tx.Queries, tx.commanderID)
}

// setShipStar moves a ship along its limit breaks as limitBreak does (the
// skills of the new template's buff_list it lacks at level 1, those only
// the old one had dropped), up or down; its level comes down to what the
// new template allows.
func (tx *Tx) setShipStar(ship *db.Ship, templateID uint32) error {
	template, ok := ownableShips[templateID]
	if !ok {
		return adminRefused("no ship template %d", templateID)
	}
	transforms, err := tx.ListShipTransforms(tx.ctx, ship.ID)
	if err != nil {
		return err
	}
	if len(transforms) > 0 {
		return adminRefused("undo the retrofit before changing the limit break")
	}
	for _, skill := range template.Skills {
		if err := tx.AddShipSkill(tx.ctx, db.AddShipSkillParams{ShipID: ship.ID, SkillID: skill, Level: 1}); err != nil {
			return err
		}
	}
	for _, skill := range ownableShips[ship.TemplateID].Skills {
		if !slices.Contains(template.Skills, skill) {
			if err := tx.DeleteShipSkill(tx.ctx, db.DeleteShipSkillParams{ShipID: ship.ID, SkillID: skill}); err != nil {
				return err
			}
		}
	}
	ship.TemplateID = templateID
	ship.MaxLevel = min(shipConfigMaxLevel, template.MaxLevel)
	if top := levelCap(templateID, 0); ship.Level > top {
		ship.Level, ship.Exp = top, 0
	}
	return nil
}

// doRetrofit does every node of the ship's retrofit tree left, in the
// tree's order, each as remouldShip does its levels (without their costs):
// the edit_trans nodes removed, the template moved, the skill taught, the
// skin put on and the archive flagged. A node needs the ship at its
// level_limit and star_limit.
func (tx *Tx) doRetrofit(ship *db.Ship) error {
	ctx := tx.ctx
	group := shipGroup(ship.TemplateID)
	nodes := transformGroups[group]
	if len(nodes) == 0 {
		return adminRefused("the ship has no retrofit")
	}
	for progress := true; progress; {
		progress = false
		rows, err := tx.ListShipTransforms(ctx, ship.ID)
		if err != nil {
			return err
		}
		levels := transformLevels(rows)
		removed := map[uint32]bool{}
		for id, level := range levels {
			if level > 0 {
				for _, gone := range transforms[id].EditTrans {
					removed[gone] = true
				}
			}
		}
		for _, id := range nodes {
			config := transforms[id]
			if removed[id] || levels[id] >= config.MaxLevel {
				continue
			}
			ready := true
			for _, condition := range config.ConditionID {
				if levels[condition] == 0 || levels[condition] != transforms[condition].MaxLevel {
					ready = false
				}
			}
			if !ready {
				continue
			}
			template := ownableShips[ship.TemplateID]
			if ship.Level < config.LevelLimit || template.Star < config.StarLimit {
				return adminRefused("the retrofit needs Lv.%d and %d stars", config.LevelLimit, config.StarLimit)
			}
			if err := tx.SetShipTransform(ctx, db.SetShipTransformParams{ShipID: ship.ID, TransformID: id, Level: config.MaxLevel}); err != nil {
				return err
			}
			for _, gone := range config.EditTrans {
				if err := tx.DeleteShipTransform(ctx, db.DeleteShipTransformParams{ShipID: ship.ID, TransformID: gone}); err != nil {
					return err
				}
			}
			if target, moves := config.transformTarget(ship.TemplateID); moves {
				next, known := ownableShips[target]
				if !known {
					return adminRefused("the retrofit moves the ship to template %d, which belfast doesn't know", target)
				}
				if err := tx.renameTemplateSkills(ship.ID, template.Skills, next.Skills); err != nil {
					return err
				}
				ship.TemplateID = target
				if err := tx.saveShipGrowth(*ship); err != nil {
					return err
				}
				if err := tx.dropUnfitSpWeapon(*ship); err != nil {
					return err
				}
			}
			if config.SkillID != 0 {
				if err := tx.AddShipSkill(ctx, db.AddShipSkillParams{ShipID: ship.ID, SkillID: config.SkillID, Level: 1}); err != nil {
					return err
				}
			}
			if config.SkinID != 0 {
				ship.SkinID = config.SkinID
				if err := tx.SetShipSkin(ctx, db.SetShipSkinParams{SkinID: config.SkinID, ID: ship.ID, CommanderID: tx.commanderID}); err != nil {
					return err
				}
				if err := tx.SetShipGroupTrans(ctx, db.SetShipGroupTransParams{CommanderID: tx.commanderID, GroupID: group}); err != nil {
					return err
				}
			}
			progress = true
			break
		}
	}
	return nil
}

// undoRetrofit takes every retrofit node off the ship, last done first: the
// template moved back (its skills renamed back), the node's skill and skin
// taken off. The archive's retrofit flag stays, as after a retirement.
func (tx *Tx) undoRetrofit(ship *db.Ship) error {
	ctx := tx.ctx
	rows, err := tx.ListShipTransforms(ctx, ship.ID)
	if err != nil {
		return err
	}
	nodes := transformGroups[shipGroup(ship.TemplateID)]
	for i := len(nodes) - 1; i >= 0; i-- {
		id := nodes[i]
		if !slices.ContainsFunc(rows, func(row db.ShipTransform) bool { return row.TransformID == id }) {
			continue
		}
		config := transforms[id]
		for _, pair := range config.ShipID {
			if len(pair) < 2 || pair[1] != ship.TemplateID {
				continue
			}
			prev, known := ownableShips[pair[0]]
			if !known {
				return adminRefused("template %d, before the retrofit, is unknown", pair[0])
			}
			if err := tx.renameTemplateSkills(ship.ID, ownableShips[ship.TemplateID].Skills, prev.Skills); err != nil {
				return err
			}
			ship.TemplateID = pair[0]
			break
		}
		if config.SkillID != 0 && !slices.Contains(ownableShips[ship.TemplateID].Skills, config.SkillID) {
			if err := tx.DeleteShipSkill(ctx, db.DeleteShipSkillParams{ShipID: ship.ID, SkillID: config.SkillID}); err != nil {
				return err
			}
		}
		if config.SkinID != 0 && ship.SkinID == config.SkinID {
			ship.SkinID = 0
			if err := tx.SetShipSkin(ctx, db.SetShipSkinParams{SkinID: 0, ID: ship.ID, CommanderID: tx.commanderID}); err != nil {
				return err
			}
		}
		if err := tx.DeleteShipTransform(ctx, db.DeleteShipTransformParams{ShipID: ship.ID, TransformID: id}); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if err := tx.DeleteShipTransform(ctx, db.DeleteShipTransformParams{ShipID: ship.ID, TransformID: row.TransformID}); err != nil {
			return err
		}
	}
	return tx.dropUnfitSpWeapon(*ship)
}

// checkRetrofitLimits refuses a ship below the level_limit or star_limit of
// a retrofit node it has done.
func checkRetrofitLimits(ctx context.Context, queries *db.Queries, ship db.Ship) error {
	rows, err := queries.ListShipTransforms(ctx, ship.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		config := transforms[row.TransformID]
		if row.Level > 0 && (ship.Level < config.LevelLimit || ownableShips[ship.TemplateID].Star < config.StarLimit) {
			return adminRefused("its retrofit needs Lv.%d and %d stars", config.LevelLimit, config.StarLimit)
		}
	}
	return nil
}

// editShipGear puts depot equipment in the ship's slots (0 empties one),
// what was there going back to the depot, as handleShipEquip; a piece must
// fit the slot (Ship:isForbiddenAtPos). Gear left in a slot the ship's new
// template can't take goes back to the depot, as the client does after a
// retrofit.
func (tx *Tx) editShipGear(ship db.Ship, gear map[uint32]uint32) error {
	ctx := tx.ctx
	slots, err := shipEquipments(ctx, tx.Queries, tx.commanderID, ship.ID)
	if err != nil {
		return err
	}
	for _, slot := range slots {
		next, changed := gear[slot.Pos]
		if !changed {
			next = slot.EquipID
			if next == 0 || fitsSlot(ship.TemplateID, next, slot.Pos) {
				continue
			}
			next = 0
		}
		if next == slot.EquipID {
			continue
		}
		if next != 0 {
			if !fitsSlot(ship.TemplateID, next, slot.Pos) {
				return adminRefused("equipment %d doesn't fit slot %d", next, slot.Pos)
			}
			if err := tx.SpendEquipment(next, 1); refused(err) {
				return adminRefused("the depot has no equipment %d", next)
			} else if err != nil {
				return err
			}
		}
		if slot.EquipID != 0 {
			if err := tx.AddEquipment(slot.EquipID, 1); err != nil {
				return err
			}
		}
		if err := tx.SetShipEquipment(ctx, db.SetShipEquipmentParams{ShipID: ship.ID, Pos: slot.Pos, EquipID: next, SkinID: slot.SkinID}); err != nil {
			return err
		}
	}
	for pos := range gear {
		if pos < 1 || pos > shipEquipSlots {
			return adminRefused("no slot %d", pos)
		}
	}
	return nil
}

// fleetMembers are a fleet's ships in order.
func fleetMembers(ctx context.Context, queries *db.Queries, commanderID, fleetID uint32) ([]uint32, error) {
	rows, err := queries.ListFleetShips(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	var ids []uint32
	for _, row := range rows {
		if row.FleetID == fleetID {
			ids = append(ids, row.ShipID)
		}
	}
	return ids, nil
}

// moveShipToFleet puts a ship in a regular fleet at slot of its team (0
// last), out of the one it was in, or out of every regular fleet with 0.
func (tx *Tx) moveShipToFleet(ship db.Ship, fleetID, slot uint32) error {
	ctx := tx.ctx
	fleets, err := regularFleets(ctx, tx.Queries, tx.commanderID)
	if err != nil {
		return err
	}
	current := fleets[ship.ID]
	if current != 0 && current != fleetID {
		members, err := fleetMembers(ctx, tx.Queries, tx.commanderID, current)
		if err != nil {
			return err
		}
		if err := tx.setFleet(current, slices.DeleteFunc(members, func(id uint32) bool { return id == ship.ID }), nil); err != nil {
			return err
		}
	}
	if fleetID == 0 {
		return nil
	}
	if !regularFleet(fleetID) {
		return adminRefused("fleet %d isn't a formation fleet", fleetID)
	}
	members, err := fleetMembers(ctx, tx.Queries, tx.commanderID, fleetID)
	if err != nil {
		return err
	}
	members = slices.DeleteFunc(members, func(id uint32) bool { return id == ship.ID })
	ships, err := shipsByID(ctx, tx.Queries, tx.commanderID, members)
	if err != nil {
		return err
	}
	team := shipTeamOf(ship)
	// its place among its team's ships, then the fleet in team order
	var mates []uint32
	for _, id := range members {
		if shipTeamOf(ships[id]) == team {
			mates = append(mates, id)
		}
	}
	at := len(mates)
	if slot >= 1 && int(slot) <= len(mates) {
		at = int(slot) - 1
	}
	mates = slices.Insert(mates, at, ship.ID)
	var order []uint32
	for _, kind := range []string{teamVanguard, teamMain, teamSubmarine} {
		if kind == team {
			order = append(order, mates...)
			continue
		}
		for _, id := range members {
			if shipTeamOf(ships[id]) == kind {
				order = append(order, id)
			}
		}
	}
	return tx.setFleet(fleetID, order, nil)
}

// setFleet stores a fleet's ships (and its cats when cats isn't nil, by
// pos: command, support; 0 empty), as the formation saves it: owned ships,
// each once and in no other regular fleet, at most three of each team, of
// the fleet's kind, no two of the same group; fleet 1 keeps a vanguard and
// a main ship (Fleet:canRemove). Cats are owned, in no other fleet, not of
// the same group.
func (tx *Tx) setFleet(fleetID uint32, shipIDs []uint32, cats []uint32) error {
	ctx := tx.ctx
	if !regularFleet(fleetID) && fleetID != fleetExercise {
		return adminRefused("fleet %d isn't a formation fleet", fleetID)
	}
	if locked, err := fleetLocked(ctx, tx.Queries, tx.commanderID, fleetID); err != nil {
		return err
	} else if locked && len(shipIDs) > 0 {
		exists, err := tx.ListFleets(ctx, tx.commanderID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(exists, func(row db.ListFleetsRow) bool { return row.FleetID == fleetID }) {
			return adminRefused("fleet %d is locked until chapter %d is cleared", fleetID, fleetUnlockChapters[fleetID])
		}
	}
	ships, err := shipsByID(ctx, tx.Queries, tx.commanderID, shipIDs)
	if err != nil {
		return err
	}
	var others map[uint32]uint32
	if regularFleet(fleetID) {
		if others, err = regularFleets(ctx, tx.Queries, tx.commanderID); err != nil {
			return err
		}
	}
	ordered, counts, err := fleetShips(ships, shipIDs, fleetID, fleetID >= fleetSubmarine && fleetID <= fleetSubmarineLast, others)
	if err != nil {
		return err
	}
	if fleetID == fleetRegular && (counts[teamVanguard] == 0 || counts[teamMain] == 0) {
		return adminRefused("fleet 1 keeps a vanguard and a main ship")
	}
	if err := tx.CreateFleet(ctx, db.CreateFleetParams{CommanderID: tx.commanderID, FleetID: fleetID, Name: ""}); err != nil {
		return err
	}
	if err := setFleetShips(ctx, tx.Queries, tx.commanderID, fleetID, ordered); err != nil {
		return err
	}
	if cats == nil {
		return nil
	}
	return tx.setFleetMeowfficers(fleetID, cats)
}

// fleetShips checks the ships a fleet is given: owned, each once and in no
// other fleet of others (ship -> fleet), at most three of each team, all
// submarines in a submarine fleet and none in the others, no two of the
// same group. It returns them in team order (vanguard, main, submarine) and
// how many of each team.
func fleetShips(ships map[uint32]db.Ship, shipIDs []uint32, fleetID uint32, submarine bool, others map[uint32]uint32) ([]uint32, map[string]int, error) {
	if len(slices.Compact(slices.Sorted(slices.Values(shipIDs)))) != len(shipIDs) {
		return nil, nil, adminRefused("a ship is in the fleet twice")
	}
	counts := map[string]int{}
	groups := map[uint32]bool{}
	var ordered []uint32
	for _, kind := range []string{teamVanguard, teamMain, teamSubmarine} {
		for _, id := range shipIDs {
			ship, ok := ships[id]
			if !ok {
				return nil, nil, adminMissing("no ship %d", id)
			}
			team := shipTeamOf(ship)
			if team != kind {
				continue
			}
			if submarine != (team == teamSubmarine) {
				return nil, nil, adminRefused("ship %d can't be in fleet %d", id, fleetID)
			}
			if others[id] != 0 && others[id] != fleetID {
				return nil, nil, adminRefused("ship %d is in fleet %d", id, others[id])
			}
			group := shipGroup(ship.TemplateID)
			if groups[group] {
				return nil, nil, adminRefused("two ships of the same kind in fleet %d", fleetID)
			}
			groups[group] = true
			counts[team]++
			if counts[team] > teamShipMax {
				return nil, nil, adminRefused("at most %d ships of a team", teamShipMax)
			}
			ordered = append(ordered, id)
		}
	}
	if len(ordered) != len(shipIDs) {
		return nil, nil, adminRefused("a ship has no team")
	}
	return ordered, counts, nil
}

func (tx *Tx) setFleetMeowfficers(fleetID uint32, cats []uint32) error {
	ctx := tx.ctx
	assigned, err := tx.ListFleetMeowfficers(ctx, tx.commanderID)
	if err != nil {
		return err
	}
	elsewhere := map[uint32]string{}
	for _, row := range assigned {
		if row.FleetID != fleetID {
			elsewhere[row.MeowfficerID] = fmt.Sprintf("fleet %d", row.FleetID)
		}
	}
	if err := checkFleetMeowfficers(ctx, tx.Queries, tx.commanderID, cats, elsewhere); err != nil {
		return err
	}
	for pos := uint32(1); pos <= meowfficerFleetSlots; pos++ {
		if err := tx.ClearFleetMeowfficer(ctx, db.ClearFleetMeowfficerParams{CommanderID: tx.commanderID, FleetID: fleetID, Pos: pos}); err != nil {
			return err
		}
	}
	for i, id := range cats {
		if id != 0 {
			if err := tx.SetFleetMeowfficer(ctx, db.SetFleetMeowfficerParams{CommanderID: tx.commanderID, FleetID: fleetID, Pos: uint32(i + 1), MeowfficerID: id}); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkFleetMeowfficers checks a fleet's cats by pos (0 empty): at most
// meowfficerFleetSlots, owned, in none of elsewhere (cat -> where it is),
// not two of the same group.
func checkFleetMeowfficers(ctx context.Context, queries *db.Queries, commanderID uint32, cats []uint32, elsewhere map[uint32]string) error {
	if len(cats) > meowfficerFleetSlots {
		return adminRefused("a fleet takes %d meowfficers", meowfficerFleetSlots)
	}
	owned, err := ownedMeowfficers(ctx, queries, commanderID)
	if err != nil {
		return err
	}
	groups := map[uint32]bool{}
	for i, id := range cats {
		if id == 0 {
			continue
		}
		cat, ok := owned[id]
		if !ok {
			return adminMissing("no meowfficer %d", id)
		}
		if where, ok := elsewhere[id]; ok {
			return adminRefused("meowfficer %d is in %s", id, where)
		}
		group := meowfficerTemplates[cat.TemplateID].GroupType
		if groups[group] || slices.Index(cats, id) != i {
			return adminRefused("two of the same meowfficer in a fleet")
		}
		groups[group] = true
	}
	return nil
}

// RemoveShip takes a ship out of the commander's dock and out of every
// table that names it: its fleets (formation, exercise, activity, elite),
// secondary secretary slots, a challenge run, the world's default
// formation, the world and guild boss rankings. Its gear and gear skins go
// back to the depot and its augment to the bag, and its archive entry keeps
// what it reached, as a retirement; it gives nothing. A ship the game holds
// elsewhere (the chapter being played, a world fleet, a commission, a guild
// mission, the main secretary, a research ship) is refused, as is the last
// vanguard or main ship of fleet 1.
func (game *Game) RemoveShip(ctx context.Context, commanderID, shipID uint32) (bool, error) {
	if _, err := adminShip(ctx, game.DB.Queries, commanderID, shipID); err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.removeShip(shipID)
	})
}

func (tx *Tx) removeShip(shipID uint32) error {
	ctx, queries, commanderID := tx.ctx, tx.Queries, tx.commanderID
	ship, err := adminShip(ctx, queries, commanderID, shipID)
	if err != nil {
		return err
	}
	inChapter, err := shipsInChapter(ctx, queries, commanderID)
	if err != nil {
		return err
	}
	if inChapter[ship.ID] {
		return adminRefused("the ship is in the chapter being played")
	}
	commissions, err := queries.ListCommissionShips(ctx, commanderID)
	if err != nil {
		return err
	}
	for _, row := range commissions {
		if row.ShipID == ship.ID {
			return adminRefused("the ship is on commission %d", row.CollectionID)
		}
	}
	if count, err := queries.AdminDockCountGuildMissionShip(ctx, db.AdminDockCountGuildMissionShipParams{CommanderID: commanderID, ShipID: ship.ID}); err != nil {
		return err
	} else if count > 0 {
		return adminRefused("the ship is on a guild operation mission")
	}
	if count, err := queries.AdminDockBlueprintOfShip(ctx, db.AdminDockBlueprintOfShipParams{CommanderID: commanderID, ShipID: ship.ID}); err != nil {
		return err
	} else if count > 0 {
		return adminRefused("a research ship stays: its blueprint is built")
	}
	secretaries, err := queries.ListSecretaries(ctx, commanderID)
	if err != nil {
		return err
	}
	for _, row := range secretaries {
		if row.ShipID == ship.ID && row.Position == 1 {
			return adminRefused("the ship is the secretary")
		}
	}

	// the world: deployed fleets hold it, the default formation forgets it
	if raw, err := queries.GetWorldState(ctx, commanderID); err == nil {
		state := &worldState{}
		if err := json.Unmarshal(raw, state); err != nil {
			return err
		}
		if slices.Contains(state.shipIDs(), ship.ID) {
			return adminRefused("the ship is in a fleet deployed in Operation Siren")
		}
		changed := false
		for i := range state.DefaultFleets {
			fleet := &state.DefaultFleets[i]
			if slices.Contains(fleet.Ships, ship.ID) {
				fleet.Ships = slices.DeleteFunc(fleet.Ships, func(id uint32) bool { return id == ship.ID })
				changed = true
			}
		}
		if changed {
			if err := tx.saveWorldState(state); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// formation and exercise fleets
	rows, err := queries.ListFleetShips(ctx, commanderID)
	if err != nil {
		return err
	}
	byFleet := map[uint32][]uint32{}
	for _, row := range rows {
		byFleet[row.FleetID] = append(byFleet[row.FleetID], row.ShipID)
	}
	for fleetID, members := range byFleet {
		if !slices.Contains(members, ship.ID) {
			continue
		}
		rest := slices.DeleteFunc(slices.Clone(members), func(id uint32) bool { return id == ship.ID })
		if fleetID == fleetRegular {
			left, err := shipsByID(ctx, queries, commanderID, rest)
			if err != nil {
				return err
			}
			team := shipTeamOf(ship)
			if !slices.ContainsFunc(rest, func(id uint32) bool { return shipTeamOf(left[id]) == team }) {
				return adminRefused("the ship is the last %s ship of fleet 1", team)
			}
		}
		if err := setFleetShips(ctx, queries, commanderID, fleetID, rest); err != nil {
			return err
		}
	}
	if exercise, err := queries.GetExercise(ctx, commanderID); err == nil {
		if slices.Contains(exercise.Vanguard, ship.ID) || slices.Contains(exercise.Main, ship.ID) {
			drop := func(id uint32) bool { return id == ship.ID }
			exercise.Vanguard = slices.DeleteFunc(exercise.Vanguard, drop)
			exercise.Main = slices.DeleteFunc(exercise.Main, drop)
			if err := queries.SaveExercise(ctx, db.SaveExerciseParams(exercise)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	// activity fleets
	activityShips, err := queries.ListActivityFleetShips(ctx, commanderID)
	if err != nil {
		return err
	}
	touched := map[uint32]bool{}
	for _, row := range activityShips {
		if row.ShipID == ship.ID {
			touched[row.ActivityID] = true
		}
	}
	for activityID := range touched {
		if err := tx.rewriteActivityFleets(activityID, activityShips, ship.ID); err != nil {
			return err
		}
	}

	// elite fleets
	if err := tx.dropFromEliteFleets(ship.ID); err != nil {
		return err
	}

	for _, fn := range []func() error{
		func() error {
			return queries.AdminDockDeleteSecretary(ctx, db.AdminDockDeleteSecretaryParams{CommanderID: commanderID, ShipID: ship.ID})
		},
		func() error {
			return queries.AdminDockDeleteChallengeShip(ctx, db.AdminDockDeleteChallengeShipParams{CommanderID: commanderID, ShipID: ship.ID})
		},
		func() error {
			return queries.AdminDockDeleteWorldBossShip(ctx, db.AdminDockDeleteWorldBossShipParams{CommanderID: commanderID, ShipID: ship.ID})
		},
		func() error {
			return queries.AdminDockDeleteGuildBossShip(ctx, db.AdminDockDeleteGuildBossShipParams{UserID: commanderID, ShipID: ship.ID})
		},
		func() error {
			return queries.AdminDockClearMarryShip(ctx, db.AdminDockClearMarryShipParams{ID: commanderID, MarryShip: ship.ID})
		},
	} {
		if err := fn(); err != nil {
			return err
		}
	}

	// what retirement does with it, without the rewards (consumeShips);
	// the slots, skills, retrofits, enhancements, dorm place, augment and
	// skin shadows go with the row (ON DELETE CASCADE)
	if err := tx.consumeShips([]db.Ship{ship}); err != nil {
		return err
	}
	return setShipTrophies(ctx, queries, commanderID)
}

// rewriteActivityFleets stores an activity's fleets again without a ship,
// the others keeping their order.
func (tx *Tx) rewriteActivityFleets(activityID uint32, rows []db.ActivityFleetShip, without uint32) error {
	ctx := tx.ctx
	if err := tx.ClearActivityFleetShips(ctx, db.ClearActivityFleetShipsParams{CommanderID: tx.commanderID, ActivityID: activityID}); err != nil {
		return err
	}
	pos := map[uint32]uint32{}
	for _, row := range rows {
		if row.ActivityID != activityID || row.ShipID == without {
			continue
		}
		if err := tx.AddActivityFleetShip(ctx, db.AddActivityFleetShipParams{
			CommanderID: tx.commanderID, ActivityID: activityID, FleetID: row.FleetID, Pos: pos[row.FleetID], ShipID: row.ShipID,
		}); err != nil {
			return err
		}
		pos[row.FleetID]++
	}
	return nil
}

// dropFromEliteFleets takes a ship out of every saved elite fleet, as
// handleRemoveEliteTargetShip does.
func (tx *Tx) dropFromEliteFleets(shipID uint32) error {
	rows, err := tx.ListChapterEliteFleets(tx.ctx, tx.commanderID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		fleet := &protobuf.FLEET_INFO{}
		if err := proto.Unmarshal(row.Fleet, fleet); err != nil {
			return err
		}
		removed := false
		for _, teams := range [][]*protobuf.TEAM_INFO{fleet.MainTeam, fleet.SubmarineTeam, fleet.SupportTeam} {
			for _, team := range teams {
				if i := slices.Index(team.ShipList, shipID); i >= 0 {
					team.ShipList = slices.Delete(team.ShipList, i, i+1)
					removed = true
				}
			}
		}
		if removed {
			if err := saveEliteFleet(tx.ctx, tx.Queries, tx.commanderID, fleet); err != nil {
				return err
			}
		}
	}
	return nil
}

// FleetView is a formation fleet: its ships in order and its cats by pos
// (command, support; 0 empty).
type FleetView struct {
	ID            uint32
	Name          string
	Ships         []uint32
	Meowfficers   []uint32
	Locked        bool
	UnlockChapter uint32   // the chapter a locked fleet waits for (Fleet:isUnlock), 0 none
	Oil           uint32   // Fleet:GetCostSum().oil
	Duplicates    []uint32 // its ships also in another fleet of its set
}

// TeamView is a team of an elite fleet.
type TeamView struct {
	Kind        string // main (a surface fleet), submarine or support
	Ships       []uint32
	Meowfficers []uint32
	Oil         uint32   // Fleet:GetCostSum().oil
	Duplicates  []uint32 // its ships also in another team of the formation
	// Chapter:singleEliteFleetVertify's refusal, "" legal: empty,
	// teamCount or typeLimitation
	Problem string
	// the team's non-free chapter_template.limitation slots, sorted as the
	// client checks them: the ship types each allows
	Limits [][]uint32
}

// EliteMax is how many teams of each kind a formation's chapter takes
// (group_num, submarine_num, support_group_num).
type EliteMax struct {
	Main, Submarine, Support uint32
}

// EliteFleetView is a chapter formation's saved fleets.
type EliteFleetView struct {
	Formation uint32
	Teams     []TeamView
	Max       EliteMax
}

// ActivityFleetView is an activity's own fleets.
type ActivityFleetView struct {
	Activity uint32
	Fleets   []FleetView
}

// FleetsView is every fleet a commander set up.
type FleetsView struct {
	Fleets   []FleetView
	Elite    []EliteFleetView
	Activity []ActivityFleetView
}

// Fleets lists the commander's formation fleets (1-6, the submarine ones,
// the exercise one once set), elite and activity fleets.
func (game *Game) Fleets(ctx context.Context, commanderID uint32) (FleetsView, error) {
	queries := game.DB.Queries
	view := FleetsView{Fleets: []FleetView{}, Elite: []EliteFleetView{}, Activity: []ActivityFleetView{}}
	rows, err := queries.ListFleets(ctx, commanderID)
	if err != nil {
		return view, err
	}
	names := map[uint32]string{}
	for _, row := range rows {
		names[row.FleetID] = row.Name
	}
	members, err := queries.ListFleetShips(ctx, commanderID)
	if err != nil {
		return view, err
	}
	cats, err := queries.ListFleetMeowfficers(ctx, commanderID)
	if err != nil {
		return view, err
	}
	owned, err := queries.ListShipsByCommander(ctx, commanderID)
	if err != nil {
		return view, err
	}
	ships := make(map[uint32]db.Ship, len(owned))
	for _, ship := range owned {
		ships[ship.ID] = ship
	}
	var ids []uint32
	for id := uint32(fleetRegular); id <= fleetSurfaceLast; id++ {
		ids = append(ids, id)
	}
	for id := uint32(fleetSubmarine); id <= fleetSubmarineLast; id++ {
		if _, ok := names[id]; ok || id <= fleetSubmarine+1 {
			ids = append(ids, id)
		}
	}
	if _, ok := names[fleetExercise]; ok {
		ids = append(ids, fleetExercise)
	}
	for _, id := range ids {
		fleet := FleetView{ID: id, Name: names[id], Ships: []uint32{}, Meowfficers: make([]uint32, meowfficerFleetSlots)}
		if _, ok := names[id]; !ok {
			if fleet.Locked, err = fleetLocked(ctx, queries, commanderID, id); err != nil {
				return view, err
			}
		}
		if fleet.Locked {
			fleet.UnlockChapter = fleetUnlockChapters[id]
		}
		for _, row := range members {
			if row.FleetID == id {
				fleet.Ships = append(fleet.Ships, row.ShipID)
			}
		}
		for _, row := range cats {
			if row.FleetID == id && row.Pos >= 1 && row.Pos <= meowfficerFleetSlots {
				fleet.Meowfficers[row.Pos-1] = row.MeowfficerID
			}
		}
		fleet.Oil = fleetOil(ships, fleet.Ships, id >= fleetSubmarine && id <= fleetSubmarineLast)
		view.Fleets = append(view.Fleets, fleet)
	}
	var regular [][]uint32
	for _, fleet := range view.Fleets {
		if regularFleet(fleet.ID) {
			regular = append(regular, fleet.Ships)
		}
	}
	shared := sharedShips(regular)
	for i := range view.Fleets {
		view.Fleets[i].Duplicates = []uint32{}
		if regularFleet(view.Fleets[i].ID) {
			view.Fleets[i].Duplicates, shared = shared[0], shared[1:]
		}
	}

	elite, err := queries.ListChapterEliteFleets(ctx, commanderID)
	if err != nil {
		return view, err
	}
	for _, row := range elite {
		fleet := &protobuf.FLEET_INFO{}
		if err := proto.Unmarshal(row.Fleet, fleet); err != nil {
			return view, err
		}
		saved := EliteFleetView{Formation: row.Formation, Teams: []TeamView{}}
		template := eliteChapter(row.Formation)
		if template != nil {
			saved.Max = EliteMax{Main: template.groupNum, Submarine: template.submarineNum, Support: template.supportNum}
		}
		var lists [][]uint32
		for _, kind := range eliteTeamKinds(fleet) {
			for i, team := range *kind.teams {
				members := append([]uint32{}, team.GetShipList()...)
				problem, limits := eliteTeamCheck(template, kind.name, uint32(i+1), members, ships)
				entry := TeamView{
					Kind: kind.name, Ships: members,
					Meowfficers: []uint32{team.GetCommanderMain(), team.GetCommanderSub()},
					Oil:         fleetOil(ships, members, kind.name == teamSubmarine),
					Problem:     problem, Limits: [][]uint32{},
				}
				for _, limit := range limits {
					entry.Limits = append(entry.Limits, limit.types())
				}
				saved.Teams = append(saved.Teams, entry)
				lists = append(lists, members)
			}
		}
		for i, duplicates := range sharedShips(lists) {
			saved.Teams[i].Duplicates = duplicates
		}
		view.Elite = append(view.Elite, saved)
	}

	activityShips, err := queries.ListActivityFleetShips(ctx, commanderID)
	if err != nil {
		return view, err
	}
	activityCats, err := queries.ListActivityFleetMeowfficers(ctx, commanderID)
	if err != nil {
		return view, err
	}
	byActivity := map[uint32]*ActivityFleetView{}
	var order []uint32
	fleetOf := func(activityID, fleetID uint32) *FleetView {
		activity := byActivity[activityID]
		if activity == nil {
			activity = &ActivityFleetView{Activity: activityID}
			byActivity[activityID] = activity
			order = append(order, activityID)
		}
		for i := range activity.Fleets {
			if activity.Fleets[i].ID == fleetID {
				return &activity.Fleets[i]
			}
		}
		activity.Fleets = append(activity.Fleets, FleetView{ID: fleetID, Ships: []uint32{}, Meowfficers: make([]uint32, meowfficerFleetSlots)})
		return &activity.Fleets[len(activity.Fleets)-1]
	}
	for _, row := range activityShips {
		fleet := fleetOf(row.ActivityID, row.FleetID)
		fleet.Ships = append(fleet.Ships, row.ShipID)
	}
	for _, row := range activityCats {
		fleet := fleetOf(row.ActivityID, row.FleetID)
		if row.Pos >= 1 && row.Pos <= meowfficerFleetSlots {
			fleet.Meowfficers[row.Pos-1] = row.MeowfficerID
		}
	}
	slices.Sort(order)
	for _, id := range order {
		activity := byActivity[id]
		slices.SortFunc(activity.Fleets, func(a, b FleetView) int { return int(a.ID) - int(b.ID) })
		var lists [][]uint32
		for i := range activity.Fleets {
			fleet := &activity.Fleets[i]
			fleet.Oil = fleetOil(ships, fleet.Ships, activityFleetSubmarine(id, fleet.ID))
			lists = append(lists, fleet.Ships)
		}
		for i, duplicates := range sharedShips(lists) {
			activity.Fleets[i].Duplicates = duplicates
		}
		view.Activity = append(view.Activity, *activity)
	}
	return view, nil
}

// FleetEdit is what the back-office changes of a formation fleet, nil
// fields kept.
type FleetEdit struct {
	Name        *string
	Ships       []uint32 // nil kept
	Meowfficers []uint32 // nil kept, by pos
}

// SetFleet edits one of the commander's formation fleets.
func (game *Game) SetFleet(ctx context.Context, commanderID, fleetID uint32, edit FleetEdit) (bool, error) {
	if edit.Name != nil && *edit.Name != "" && !validLabel(*edit.Name, minFleetNameLength, maxFleetNameLength) {
		return false, adminRefused("a fleet name takes %d to %d characters", minFleetNameLength, maxFleetNameLength)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		ships := edit.Ships
		if ships == nil {
			current, err := fleetMembers(ctx, tx.Queries, commanderID, fleetID)
			if err != nil {
				return err
			}
			ships = current
		}
		if err := tx.setFleet(fleetID, ships, edit.Meowfficers); err != nil {
			return err
		}
		if edit.Name != nil {
			return tx.RenameFleet(ctx, db.RenameFleetParams{CommanderID: commanderID, FleetID: fleetID, Name: *edit.Name})
		}
		return nil
	})
}

// ClearFleet empties a formation fleet of its ships and cats; fleet 1 keeps
// a ship of each team, so it can't be.
func (game *Game) ClearFleet(ctx context.Context, commanderID, fleetID uint32) (bool, error) {
	if fleetID == fleetRegular {
		return false, adminRefused("fleet 1 keeps a vanguard and a main ship")
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.setFleet(fleetID, []uint32{}, make([]uint32, meowfficerFleetSlots))
	})
}

// ClearEliteFleet forgets a chapter formation's saved fleets: the chapters
// start from empty fleets again.
func (game *Game) ClearEliteFleet(ctx context.Context, commanderID, formation uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		n, err := tx.AdminDockDeleteEliteFleet(ctx, db.AdminDockDeleteEliteFleetParams{CommanderID: commanderID, Formation: formation})
		if err == nil && n == 0 {
			return adminMissing("no fleets saved for formation %d", formation)
		}
		return err
	})
}

// ClearActivityFleet forgets an activity's own fleets (ships and cats).
func (game *Game) ClearActivityFleet(ctx context.Context, commanderID, activityID uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		if err := tx.ClearActivityFleetShips(ctx, db.ClearActivityFleetShipsParams{CommanderID: commanderID, ActivityID: activityID}); err != nil {
			return err
		}
		return tx.ClearActivityFleetMeowfficers(ctx, db.ClearActivityFleetMeowfficersParams{CommanderID: commanderID, ActivityID: activityID})
	})
}

// fleetOil is Fleet:GetCostSum().oil: the start (Fleet:getStartCost) and
// end (Fleet:getEndCost) oil of its ships, the end alone for a submarine
// fleet.
func fleetOil(ships map[uint32]db.Ship, ids []uint32, submarine bool) uint32 {
	members := map[uint32]db.Ship{}
	for _, id := range ids {
		if ship, ok := ships[id]; ok {
			members[id] = ship
		}
	}
	oil := battleEndOil(members)
	if !submarine {
		oil += battleStartOil(members)
	}
	return oil
}

// sharedShips are, for each fleet of a set, its ships also in another one.
func sharedShips(fleets [][]uint32) [][]uint32 {
	in := map[uint32]int{}
	for _, ships := range fleets {
		for _, id := range slices.Compact(slices.Sorted(slices.Values(ships))) {
			in[id]++
		}
	}
	shared := make([][]uint32, len(fleets))
	for i, ships := range fleets {
		shared[i] = []uint32{}
		for _, id := range ships {
			if in[id] > 1 && !slices.Contains(shared[i], id) {
				shared[i] = append(shared[i], id)
			}
		}
	}
	return shared
}

// eliteTeamKind is one kind of an elite fleet's teams (FLEET_INFO
// main_team, submarine_team, support_team).
type eliteTeamKind struct {
	name  string
	teams *[]*protobuf.TEAM_INFO
}

func eliteTeamKinds(fleet *protobuf.FLEET_INFO) []eliteTeamKind {
	return []eliteTeamKind{{teamMain, &fleet.MainTeam}, {teamSubmarine, &fleet.SubmarineTeam}, {teamSupport, &fleet.SupportTeam}}
}

// teamSupport is FleetType.Support's teams.
const teamSupport = "support"

// eliteChapter is the chapter of a formation the back-office checks its
// saved fleets against: the formation's chapters share them and the client
// checks them against the one being opened, so the one taking the most
// teams, the last of those (usually the strictest), nil when none.
func eliteChapter(formation uint32) *chapterTemplate {
	var best *chapterTemplate
	for _, template := range chapterTemplates {
		if template.formation != formation {
			continue
		}
		teams := template.groupNum + template.submarineNum + template.supportNum
		if best == nil {
			best = template
			continue
		}
		if bestTeams := best.groupNum + best.submarineNum + best.supportNum; teams > bestTeams || teams == bestTeams && template.id > best.id {
			best = template
		}
	}
	return best
}

// eliteTeamMax is how many teams of kind a formation's chapter takes.
func eliteTeamMax(template *chapterTemplate, kind string) uint32 {
	switch kind {
	case teamMain:
		return template.groupNum
	case teamSubmarine:
		return template.submarineNum
	case teamSupport:
		return template.supportNum
	}
	return 0
}

// types are the ship types a limitation slot allows
// (ShipType.GetShipTypesFromLimit), none for a free one.
func (limit shipLimit) types() []uint32 {
	if limit.bundle != "" {
		return slices.Clone(shipBundles[limit.bundle])
	}
	if limit.shipType == 0 {
		return nil
	}
	return []uint32{limit.shipType}
}

// eliteTeamLimits are a team's chapter_template.limitation slots as
// Chapter:singleEliteFleetVertify reads them: the entry of the team's index
// among all of them (Chapter:IsEliteFleetLegal: surface teams first, the
// submarine ones after 2, the support ones after 3), flattened, the free
// slots dropped, sorted by ChapterProxy.SortRecommendLimitation (ship types
// highest first, then bundles).
func eliteTeamLimits(template *chapterTemplate, kind string, index uint32) []shipLimit {
	if template == nil {
		return nil
	}
	offset := map[string]uint32{teamMain: 0, teamSubmarine: 2, teamSupport: 3}[kind]
	at := int(offset + index - 1)
	if at >= len(template.limitation) {
		return nil
	}
	var limits []shipLimit
	for _, half := range template.limitation[at] {
		for _, limit := range half {
			if limit.bundle != "" || limit.shipType != 0 {
				limits = append(limits, limit)
			}
		}
	}
	slices.SortStableFunc(limits, func(a, b shipLimit) int { return a.order() - b.order() })
	return limits
}

// eliteTeamCheck is Chapter:singleEliteFleetVertify for the team at index
// (1-based) of kind: "" when the chapter can sortie with it, else why not
// (empty, teamCount: a surface team needs one to three main and vanguard
// ships, typeLimitation: its ships don't fill the chapter's limitation).
// The client also refuses a ship on an event (inEvent), which belfast
// doesn't track. It returns the team's limitation slots too.
func eliteTeamCheck(template *chapterTemplate, kind string, index uint32, ids []uint32, ships map[uint32]db.Ship) (string, []shipLimit) {
	limits := eliteTeamLimits(template, kind, index)
	if len(ids) == 0 {
		return "empty", limits
	}
	counts := map[string]int{}
	var types []uint32
	for _, id := range ids {
		ship, ok := ships[id]
		if !ok {
			continue
		}
		counts[shipTeamOf(ship)]++
		types = append(types, ownableShips[ship.TemplateID].Type)
	}
	if kind == teamMain && (counts[teamMain] > teamShipMax || counts[teamVanguard] > teamShipMax || counts[teamMain]*counts[teamVanguard] == 0) {
		return "teamCount", limits
	}
	// each ship takes the first slot left that allows its type, or is
	// dropped
	free := slices.Clone(limits)
	var seated []uint32
	for _, shipType := range types {
		if i := slices.IndexFunc(free, func(limit shipLimit) bool { return limit.allows(shipType) }); i >= 0 {
			free = slices.Delete(free, i, i+1)
			seated = append(seated, shipType)
		}
	}
	if kind != teamMain {
		if len(free) == 0 || len(seated) > 0 {
			return "", limits
		}
		return "typeLimitation", limits
	}
	// a surface team keeps a seated ship of each row, unless no slot left
	// asks for that row
	for _, team := range []string{teamVanguard, teamMain} {
		has := slices.ContainsFunc(seated, func(shipType uint32) bool { return shipTeams[shipType] == team })
		asked := slices.ContainsFunc(free, func(limit shipLimit) bool {
			return slices.ContainsFunc(limit.types(), func(shipType uint32) bool { return shipTeams[shipType] == team })
		})
		if !has && asked {
			return "typeLimitation", limits
		}
	}
	return "", limits
}

// activity_template types whose fleets aren't told apart by
// Fleet.isSubmarineFleet (FleetProxy:addActivityFleet)
const (
	activityTypeBossRush           = 93
	activityTypeBossSingle         = 111
	activityTypeBossSingleVariable = 123
	megaSubmarineFleetOffset       = 100 // Fleet.MEGA_SUBMARINE_FLEET_OFFSET
	submarineFleetNums             = 4   // Fleet.SUBMARINE_FLEET_NUMS
)

// bossRushSeriesFleets are the number of expeditions of each boss rush
// series (activity_series_enemy expedition_id; a collab series' first
// difficulty, extraenemy_series_template activity_series_enemy_id, its
// difficulties having as many), by series and by whether it's a collab's.
var bossRushSeriesFleets = sync.OnceValues(func() (map[bool]map[uint32]int, error) {
	series, err := gamedata.Table[struct {
		ExpeditionID []uint32 `json:"expedition_id"`
	}]("activity_series_enemy")
	if err != nil {
		return nil, err
	}
	collab, err := gamedata.Table[struct {
		SeriesEnemy []uint32 `json:"activity_series_enemy_id"`
	}]("extraenemy_series_template")
	if err != nil {
		return nil, err
	}
	counts := map[bool]map[uint32]int{false: {}, true: {}}
	for id, row := range series {
		counts[false][id] = len(row.ExpeditionID)
	}
	for id, row := range collab {
		if len(row.SeriesEnemy) > 0 {
			counts[true][id] = len(series[row.SeriesEnemy[0]].ExpeditionID)
		}
	}
	return counts, nil
})

// activityFleetSubmarine is whether an activity's fleet is a submarine one
// (FleetProxy:addActivityFleet's fleetType): a boss rush's are the last of
// each series' fleets (BossRushSeriesData.StaticCalculateFleetIds: series x
// 10 + 0 to its expedition count), the boss battles' from
// SUBMARINE_FLEET_ID or MEGA_SUBMARINE_FLEET_OFFSET, the others
// Fleet.isSubmarineFleet's.
func activityFleetSubmarine(activityID, fleetID uint32) bool {
	config := activityConfigs[activityID]
	switch config.Type {
	case activityTypeBossRush, activityTypeBossRushDalCollab:
		counts, err := bossRushSeriesFleets()
		if err != nil {
			return false
		}
		for _, series := range gamedata.IDs(config.ConfigData) {
			if count, ok := counts[config.Type == activityTypeBossRushDalCollab][series]; ok && fleetID == series*10+uint32(count) {
				return true
			}
		}
		return false
	case activityTypeBossBattleMark2, activityTypeBossSingle:
		return fleetID >= fleetSubmarine
	case activityTypeBossSingleVariable:
		return fleetID >= megaSubmarineFleetOffset
	}
	return fleetID >= fleetSubmarine && fleetID < fleetSubmarine+submarineFleetNums
}

// SetEliteTeam replaces the ships (in order) and cats ([command, support])
// of the team at index (1-based) of kind (main, submarine or support) of a
// formation's saved elite fleets, nil kept; a team past the saved ones is
// added, empty ones before it. The ships are owned, in no other team of the
// formation, at most three main and three vanguard ships in a main team,
// three submarines in a submarine team, three ships in a support team; the
// cats are owned, in no other team, not of the same group. The chapter's
// limitation isn't checked: the client saves any, it refuses to sortie
// (see eliteTeamCheck).
func (game *Game) SetEliteTeam(ctx context.Context, commanderID, formation uint32, kind string, index uint32, shipIDs, cats []uint32) (bool, error) {
	template := eliteChapter(formation)
	if template == nil {
		return false, adminMissing("no chapter has formation %d", formation)
	}
	if kind != teamMain && kind != teamSubmarine && kind != teamSupport {
		return false, adminRefused("no %q teams: main, submarine or support", kind)
	}
	if most := eliteTeamMax(template, kind); index == 0 || index > most {
		return false, adminRefused("formation %d takes %d %s teams", formation, most, kind)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		rows, err := tx.ListChapterEliteFleets(ctx, commanderID)
		if err != nil {
			return err
		}
		at := slices.IndexFunc(rows, func(row db.ChapterEliteFleet) bool { return row.Formation == formation })
		if at < 0 {
			return adminMissing("no fleets saved for formation %d", formation)
		}
		fleet := &protobuf.FLEET_INFO{}
		if err := proto.Unmarshal(rows[at].Fleet, fleet); err != nil {
			return err
		}
		fleet.Id = proto.Uint32(formation)
		var target **protobuf.TEAM_INFO
		otherShips := map[uint32]bool{}
		otherCats := map[uint32]string{}
		for _, teams := range eliteTeamKinds(fleet) {
			if teams.name == kind {
				for uint32(len(*teams.teams)) < index {
					*teams.teams = append(*teams.teams, &protobuf.TEAM_INFO{Id: proto.Uint32(0)})
				}
			}
			for i, team := range *teams.teams {
				if teams.name == kind && uint32(i+1) == index {
					target = &(*teams.teams)[i]
					continue
				}
				for _, id := range team.GetShipList() {
					otherShips[id] = true
				}
				for _, id := range []uint32{team.GetCommanderMain(), team.GetCommanderSub()} {
					if id != 0 {
						otherCats[id] = fmt.Sprintf("the %s team %d", teams.name, i+1)
					}
				}
			}
		}
		team := *target
		if shipIDs != nil {
			if len(slices.Compact(slices.Sorted(slices.Values(shipIDs)))) != len(shipIDs) {
				return adminRefused("a ship is in the team twice")
			}
			ships, err := shipsByID(ctx, tx.Queries, commanderID, shipIDs)
			if err != nil {
				return err
			}
			counts := map[string]int{}
			for _, id := range shipIDs {
				ship, ok := ships[id]
				if !ok {
					return adminMissing("no ship %d", id)
				}
				if otherShips[id] {
					return adminRefused("ship %d is in another team of formation %d", id, formation)
				}
				shipTeam := shipTeamOf(ship)
				switch {
				case kind == teamMain && shipTeam == teamSubmarine:
					return adminRefused("ship %d is a submarine", id)
				case kind == teamSubmarine && shipTeam != teamSubmarine:
					return adminRefused("ship %d isn't a submarine", id)
				}
				counts[shipTeam]++
				if counts[shipTeam] > teamShipMax || kind == teamSupport && len(shipIDs) > teamShipMax {
					return adminRefused("at most %d ships of a team", teamShipMax)
				}
			}
			team.ShipList = slices.Clone(shipIDs)
		}
		if cats != nil {
			if err := checkFleetMeowfficers(ctx, tx.Queries, commanderID, cats, otherCats); err != nil {
				return err
			}
			slots := make([]uint32, meowfficerFleetSlots)
			copy(slots, cats)
			team.CommanderMain, team.CommanderSub = proto.Uint32(slots[0]), proto.Uint32(slots[1])
		}
		return saveEliteFleet(ctx, tx.Queries, commanderID, fleet)
	})
}

// SetActivityFleet replaces the ships and cats (by pos, nil kept) of one of
// an activity's own fleets, with the rules of a formation fleet (fleetShips):
// owned ships in no other fleet of the activity, of the fleet's kind
// (activityFleetSubmarine), at most three of each team, no two of the same
// group; cats owned, in no other fleet of the activity, not of the same
// group.
func (game *Game) SetActivityFleet(ctx context.Context, commanderID, activityID, fleetID uint32, shipIDs, cats []uint32) (bool, error) {
	if _, ok := activityConfigs[activityID]; !ok {
		return false, adminMissing("no activity %d", activityID)
	}
	if fleetID == 0 {
		return false, adminRefused("no fleet 0")
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		if shipIDs != nil {
			rows, err := tx.ListActivityFleetShips(ctx, commanderID)
			if err != nil {
				return err
			}
			others := map[uint32]uint32{}
			for _, row := range rows {
				if row.ActivityID == activityID && row.FleetID != fleetID {
					others[row.ShipID] = row.FleetID
				}
			}
			ships, err := shipsByID(ctx, tx.Queries, commanderID, shipIDs)
			if err != nil {
				return err
			}
			ordered, _, err := fleetShips(ships, shipIDs, fleetID, activityFleetSubmarine(activityID, fleetID), others)
			if err != nil {
				return err
			}
			key := db.AdminDockClearActivityFleetShipsParams{CommanderID: commanderID, ActivityID: activityID, FleetID: fleetID}
			if err := tx.AdminDockClearActivityFleetShips(ctx, key); err != nil {
				return err
			}
			for i, id := range ordered {
				err := tx.AddActivityFleetShip(ctx, db.AddActivityFleetShipParams{
					CommanderID: commanderID, ActivityID: activityID, FleetID: fleetID, Pos: uint32(i), ShipID: id,
				})
				if err != nil {
					return err
				}
			}
		}
		if cats == nil {
			return nil
		}
		rows, err := tx.ListActivityFleetMeowfficers(ctx, commanderID)
		if err != nil {
			return err
		}
		elsewhere := map[uint32]string{}
		for _, row := range rows {
			if row.ActivityID == activityID && row.FleetID != fleetID {
				elsewhere[row.MeowfficerID] = fmt.Sprintf("fleet %d", row.FleetID)
			}
		}
		if err := checkFleetMeowfficers(ctx, tx.Queries, commanderID, cats, elsewhere); err != nil {
			return err
		}
		key := db.AdminDockClearActivityFleetMeowfficersParams{CommanderID: commanderID, ActivityID: activityID, FleetID: fleetID}
		if err := tx.AdminDockClearActivityFleetMeowfficers(ctx, key); err != nil {
			return err
		}
		for i, id := range cats {
			if id != 0 {
				err := tx.AddActivityFleetMeowfficer(ctx, db.AddActivityFleetMeowfficerParams{
					CommanderID: commanderID, ActivityID: activityID, FleetID: fleetID, Pos: uint32(i + 1), MeowfficerID: id,
				})
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// MeowfficerView is one of the commander's cats.
type MeowfficerView struct {
	db.Meowfficer
	Rarity      uint32
	Nationality uint32
	ExpNext     uint32 // EXP from its level to the next, 0 at the last
	MaxLevel    uint32
	Fleet       uint32 // the formation fleet it's in, 0 none
	Cattery     uint32 // the cattery slot it lives in, 0 none
	// its skill's level (commander_skill_template lv by its chain), last
	// level, and the EXP from its level to the next (0 at the last)
	SkillLevel, SkillMaxLevel, SkillExpNext uint32
	// the EXP each level takes to the next, from Lv.1 (0 at the last): its
	// own and its skill's
	LevelExps, SkillLevelExps []uint32
}

// MeowfficerBoxView is a cat box training.
type MeowfficerBoxView struct {
	db.MeowfficerBox
	Ready bool
}

// CatterySlotView is a cattery slot open at its level, with the ops done
// today.
type CatterySlotView struct {
	ID           uint32
	MeowfficerID uint32
	Style        uint32
	Cleaned      bool
	Fed          bool
	Played       bool
}

// MeowfficerPresetView is a saved pair.
type MeowfficerPresetView struct {
	ID          uint32
	Name        string
	Meowfficers []uint32
}

// MeowfficersView is the commander's cats, boxes, cattery and pairs.
type MeowfficersView struct {
	Meowfficers  []MeowfficerView
	Capacity     uint32
	Boxes        []MeowfficerBoxView
	BoxCount     uint32
	BoughtToday  uint32
	BoughtTotal  uint32
	CatteryLevel uint32
	CatteryExp   uint32
	CatteryNext  uint32 // home_exp, the EXP to the next level
	Clean        uint32
	CleanMax     uint32 // the top commander_home flower[3] threshold
	Slots        []CatterySlotView
	Presets      []MeowfficerPresetView
	Templates    []uint32 // the cats that can be granted
}

// catteryCleanLevels is commander_home flower[3], the cleanliness
// thresholds (CommanderHome:GetCleanLevel).
func catteryCleanLevels(home catteryLevel) []uint32 {
	var flower []json.RawMessage
	var levels []uint32
	if json.Unmarshal(home.Flower, &flower) == nil && len(flower) > 2 {
		json.Unmarshal(flower[2], &levels)
	}
	return levels
}

// Meowfficers is the commander's cats, boxes, cattery and pairs.
func (game *Game) Meowfficers(ctx context.Context, commanderID uint32) (MeowfficersView, error) {
	queries := game.DB.Queries
	view := MeowfficersView{Meowfficers: []MeowfficerView{}, Boxes: []MeowfficerBoxView{}, Slots: []CatterySlotView{}, Presets: []MeowfficerPresetView{}, BoxCount: meowfficerBoxCount}
	commander, err := queries.GetCommander(ctx, commanderID)
	if errors.Is(err, sql.ErrNoRows) {
		return view, adminMissing("no commander %d", commanderID)
	} else if err != nil {
		return view, err
	}
	view.Capacity = commander.CommanderBagMax
	now := time.Now()
	fleetCats, err := queries.ListFleetMeowfficers(ctx, commanderID)
	if err != nil {
		return view, err
	}
	slots, err := queries.ListMeowfficerCatteries(ctx, commanderID)
	if err != nil {
		return view, err
	}
	cats, err := queries.ListMeowfficers(ctx, commanderID)
	if err != nil {
		return view, err
	}
	top := meowfficerMaxLevel()
	for _, cat := range cats {
		template := meowfficerTemplates[cat.TemplateID]
		row := MeowfficerView{Meowfficer: cat, Rarity: template.Rarity, MaxLevel: top}
		chain := meowfficerSkillChain(cat.SkillID)
		row.SkillLevel, row.SkillMaxLevel = uint32(slices.Index(chain, cat.SkillID)+1), uint32(len(chain))
		if !meowfficerSkillMaxed(cat.SkillID) {
			row.SkillExpNext = meowfficerSkillLevels[cat.SkillID].Exp
		}
		for _, id := range chain {
			row.SkillLevelExps = append(row.SkillLevelExps, meowfficerSkillLevels[id].Exp)
		}
		for level := uint32(1); level <= top; level++ {
			need := uint32(0)
			if level < top {
				need = meowfficerLevelExp(level, template.Rarity)
			}
			row.LevelExps = append(row.LevelExps, need)
		}
		row.Nationality = template.Nationality
		if cat.Level < top {
			row.ExpNext = meowfficerLevelExp(cat.Level, template.Rarity)
		}
		for _, assigned := range fleetCats {
			if assigned.MeowfficerID == cat.ID {
				row.Fleet = assigned.FleetID
			}
		}
		for _, slot := range slots {
			if slot.MeowfficerID == cat.ID {
				row.Cattery = slot.SlotID
			}
		}
		view.Meowfficers = append(view.Meowfficers, row)
	}

	boxes, err := queries.ListMeowfficerBoxes(ctx, commanderID)
	if err != nil {
		return view, err
	}
	for _, box := range boxes {
		view.Boxes = append(view.Boxes, MeowfficerBoxView{MeowfficerBox: box, Ready: box.FinishTime <= now.Unix()})
	}
	state, err := meowfficerState(ctx, queries, commanderID)
	if err != nil {
		return view, err
	}
	view.BoughtToday = boxesBoughtToday(state, now)
	view.BoughtTotal = state.ReserveTotal
	home := cattery(state.HomeLevel)
	view.CatteryLevel, view.CatteryExp, view.CatteryNext, view.Clean = state.HomeLevel, state.HomeExp, home.HomeExp, state.HomeClean
	if levels := catteryCleanLevels(home); len(levels) > 0 {
		view.CleanMax = slices.Max(levels)
	}
	for id := uint32(1); id <= max(home.NestNumber, 1) && id <= catterySlotCount; id++ {
		slot := catterySlot(slots, commanderID, id)
		flag := catteryOpFlag(slot, now)
		view.Slots = append(view.Slots, CatterySlotView{
			ID: id, MeowfficerID: slot.MeowfficerID, Style: slot.Style,
			Cleaned: flag&catteryOpBits[catteryClean] == 0, Fed: flag&catteryOpBits[catteryFeed] == 0, Played: flag&catteryOpBits[catteryPlay] == 0,
		})
	}

	presets, err := queries.ListMeowfficerPresets(ctx, commanderID)
	if err != nil {
		return view, err
	}
	presetSlots, err := queries.ListMeowfficerPresetSlots(ctx, commanderID)
	if err != nil {
		return view, err
	}
	for id := uint32(1); id <= meowfficerPresetCount; id++ {
		preset := MeowfficerPresetView{ID: id, Meowfficers: make([]uint32, meowfficerFleetSlots)}
		for _, row := range presets {
			if row.PresetID == id {
				preset.Name = row.Name
			}
		}
		for _, row := range presetSlots {
			if row.PresetID == id && row.Pos >= 1 && row.Pos <= meowfficerFleetSlots {
				preset.Meowfficers[row.Pos-1] = row.MeowfficerID
			}
		}
		view.Presets = append(view.Presets, preset)
	}
	view.Templates = sortedKeys(meowfficerTemplates)
	return view, nil
}

// MeowfficerEdit is what the back-office changes of a cat, nil fields kept.
type MeowfficerEdit struct {
	Name   *string // "" for its template's
	Level  *uint32
	Exp    *uint32
	Locked *bool
	// its skill's level (1 to the last of its chain) and EXP toward the next
	SkillLevel *uint32
	SkillExp   *uint32
}

// EditMeowfficer edits one of the commander's cats: its name (no spaces, at
// most 20 characters, RenameCommanderCommand's check; no cooldown), its
// level up to the last commander_level and its EXP below the level's,
// its skill's level (the chain's skill id, CommanderSkill:addExp moving
// along next_id) and EXP below the level's, its lock.
func (game *Game) EditMeowfficer(ctx context.Context, commanderID, catID uint32, edit MeowfficerEdit) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		cat, err := tx.GetMeowfficer(ctx, db.GetMeowfficerParams{CommanderID: commanderID, ID: catID})
		if errors.Is(err, sql.ErrNoRows) {
			return adminMissing("commander %d has no meowfficer %d", commanderID, catID)
		} else if err != nil {
			return err
		}
		if edit.Name != nil && *edit.Name != cat.Name {
			name := *edit.Name
			if name != "" && !validMeowfficerName(name) {
				return adminRefused("a meowfficer name takes at most %d characters and no spaces", meowfficerNameMaxLength)
			}
			if err := tx.AdminDockSetMeowfficerName(ctx, db.AdminDockSetMeowfficerNameParams{Name: name, CommanderID: commanderID, ID: cat.ID}); err != nil {
				return err
			}
		}
		if edit.Level != nil || edit.Exp != nil {
			level, exp := cat.Level, cat.Exp
			if edit.Level != nil {
				if *edit.Level < 1 || *edit.Level > meowfficerMaxLevel() {
					return adminRefused("a meowfficer goes from level 1 to %d", meowfficerMaxLevel())
				}
				if *edit.Level != level {
					level, exp = *edit.Level, 0
				}
			}
			if edit.Exp != nil {
				exp = *edit.Exp
			}
			need := meowfficerLevelExp(level, meowfficerTemplates[cat.TemplateID].Rarity)
			if level < meowfficerMaxLevel() && exp >= need {
				return adminRefused("Lv.%d takes %d EXP", level, need)
			}
			if level >= meowfficerMaxLevel() {
				exp = 0
			}
			if err := tx.SetMeowfficerLevel(ctx, db.SetMeowfficerLevelParams{Level: level, Exp: exp, CommanderID: commanderID, ID: cat.ID}); err != nil {
				return err
			}
		}
		if edit.SkillLevel != nil || edit.SkillExp != nil {
			skill, exp := cat.SkillID, cat.SkillExp
			if edit.SkillLevel != nil {
				chain := meowfficerSkillChain(cat.SkillID)
				if *edit.SkillLevel < 1 || int(*edit.SkillLevel) > len(chain) {
					return adminRefused("its skill goes from level 1 to %d", len(chain))
				}
				if next := chain[*edit.SkillLevel-1]; next != skill {
					skill, exp = next, 0
				}
			}
			if edit.SkillExp != nil {
				exp = *edit.SkillExp
			}
			// a level some skills reach for 0 EXP holds none
			if need := meowfficerSkillLevels[skill].Exp; !meowfficerSkillMaxed(skill) && exp > 0 && exp >= need {
				return adminRefused("its skill's level takes %d EXP", need)
			}
			if err := tx.SetMeowfficerSkill(ctx, db.SetMeowfficerSkillParams{SkillID: skill, SkillExp: exp, CommanderID: commanderID, ID: cat.ID}); err != nil {
				return err
			}
		}
		if edit.Locked != nil && *edit.Locked != cat.Locked {
			return tx.SetMeowfficerLocked(ctx, db.SetMeowfficerLockedParams{Locked: *edit.Locked, CommanderID: commanderID, ID: cat.ID})
		}
		return nil
	})
}

// validMeowfficerName is RenameCommanderCommand's nameValidityCheck(name,
// 0, 20): not empty, no spaces, at most 20 characters.
func validMeowfficerName(name string) bool {
	return validLabel(name, 1, meowfficerNameMaxLength) && !containsSpace(name)
}

func containsSpace(name string) bool {
	for _, r := range name {
		if r == ' ' || r == '\t' || r == '\n' || r == '　' {
			return true
		}
	}
	return false
}

// FinishMeowfficerBox finishes a box now, as a quick finisher would: the
// ones queued behind move up.
func (game *Game) FinishMeowfficerBox(ctx context.Context, commanderID, boxID uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		boxes, err := tx.ListMeowfficerBoxes(ctx, commanderID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		queue := boxQueue(boxes)
		i := slices.IndexFunc(queue, func(box db.MeowfficerBox) bool { return box.BoxID == boxID })
		if i < 0 {
			return adminMissing("box %d is empty", boxID)
		}
		if queue[i].FinishTime <= now {
			return adminRefused("box %d is done already", boxID)
		}
		queue[i].BeginTime, queue[i].FinishTime = min(queue[i].BeginTime, now), now
		return rescheduleBoxes(tx, boxQueue(queue), now)
	})
}

// EmptyMeowfficerBox throws a box away with the cat it holds; the ones
// queued behind move up.
func (game *Game) EmptyMeowfficerBox(ctx context.Context, commanderID, boxID uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		boxes, err := tx.ListMeowfficerBoxes(ctx, commanderID)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(boxes, func(box db.MeowfficerBox) bool { return box.BoxID == boxID }) {
			return adminMissing("box %d is empty", boxID)
		}
		if err := tx.DeleteMeowfficerBox(ctx, db.DeleteMeowfficerBoxParams{CommanderID: commanderID, BoxID: boxID}); err != nil {
			return err
		}
		rest := slices.DeleteFunc(boxes, func(box db.MeowfficerBox) bool { return box.BoxID == boxID })
		return rescheduleBoxes(tx, boxQueue(rest), time.Now().Unix())
	})
}

// FillCatteryClean sets the cattery's cleanliness to its top level
// (commander_home flower[3]'s last threshold).
func (game *Game) FillCatteryClean(ctx context.Context, commanderID uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		state, err := meowfficerState(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		levels := catteryCleanLevels(cattery(state.HomeLevel))
		if len(levels) == 0 {
			return adminRefused("the cattery's level has no cleanliness levels")
		}
		if err := tx.CreateMeowfficerState(ctx, commanderID); err != nil {
			return err
		}
		return tx.SetMeowfficerHomeClean(ctx, db.SetMeowfficerHomeCleanParams{HomeClean: slices.Max(levels), CleanTime: time.Now().Unix(), CommanderID: commanderID})
	})
}

// ResetCatteryOps makes today's clean, feed and play available again in
// every slot and for every cat.
func (game *Game) ResetCatteryOps(ctx context.Context, commanderID uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		slots, err := tx.ListMeowfficerCatteries(ctx, commanderID)
		if err != nil {
			return err
		}
		for _, slot := range slots {
			slot.CleanTime, slot.FeedTime, slot.PlayTime = 0, 0, 0
			if err := saveCatterySlot(tx, slot); err != nil {
				return err
			}
		}
		cats, err := tx.ListMeowfficers(ctx, commanderID)
		if err != nil {
			return err
		}
		for _, cat := range cats {
			params := []error{
				tx.SetMeowfficerHomeCleanTime(ctx, db.SetMeowfficerHomeCleanTimeParams{HomeCleanTime: 0, CommanderID: commanderID, ID: cat.ID}),
				tx.SetMeowfficerHomeFeedTime(ctx, db.SetMeowfficerHomeFeedTimeParams{HomeFeedTime: 0, CommanderID: commanderID, ID: cat.ID}),
				tx.SetMeowfficerHomePlayTime(ctx, db.SetMeowfficerHomePlayTimeParams{HomePlayTime: 0, CommanderID: commanderID, ID: cat.ID}),
			}
			if err := errors.Join(params...); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearMeowfficerPreset empties a saved pair, its name kept.
func (game *Game) ClearMeowfficerPreset(ctx context.Context, commanderID, presetID uint32) (bool, error) {
	if presetID < 1 || presetID > meowfficerPresetCount {
		return false, adminMissing("no preset %d", presetID)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.ClearMeowfficerPresetSlots(ctx, db.ClearMeowfficerPresetSlotsParams{CommanderID: commanderID, PresetID: presetID})
	})
}

// GrantMeowfficer gives the commander a cat of a commander_data_template,
// as a reward does (DROP_TYPE_COMMANDER_CAT, Grant): its starting talents,
// a special one owned already turning into its coins.
func (game *Game) GrantMeowfficer(ctx context.Context, commanderID, templateID uint32) (bool, error) {
	if _, ok := meowfficerTemplates[templateID]; !ok {
		return false, adminMissing("no meowfficer template %d", templateID)
	}
	return game.Grant(ctx, commanderID, []*protobuf.DROPINFO{{Type: proto.Uint32(dropTypeMeowfficer), Id: proto.Uint32(templateID), Number: proto.Uint32(1)}})
}
