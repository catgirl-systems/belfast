package game

import (
	"encoding/json"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"

	"github.com/ggmolly/belfast/internal/logger"
)

// The events' effects (world_effect_data), run one per OpReqEvent: the
// client sends the event's cell when its next effect is due (WSCommand:
// OpEvent, once the effect's own client part ran: a story, a guide, a
// choice...), the server takes that effect off the event's effect_list,
// applies it and sends the cell back; the client then applies its side
// (WorldMapOp:Apply, OpTriggerEvent) and asks for the next one
// (WorldMap:CheckInteractive: an autoactivate effect anywhere on the map,
// else the event under the fleet). An event whose effects are all done
// leaves the map (an event attachment is always alive,
// WorldMapAttachment:IsAlive, so the server takes it off).
//
// The effect types the client names (WorldMapAttachment.EffectEvent*) are
// applied as its WorldMapOp:Apply does; the others are server-side and
// only their parameters say what they do, so this is a reading of the
// tables, unverified:
//   - 1 / 12 {enemy, -1}: an enemy appears on the event's cell, to be
//     fought before the event goes on (the alive enemy comes first in
//     CheckInteractive);
//   - 5 {task, chance}: a world task is handed out (phase 4 hook);
//   - 9 {event ids}: the events appear on their grid (one per grid entry;
//     a grid with negative entries is offsets from the event's cell);
//   - 10 {event ids}: the same, the grid always offsets from the event;
//   - 11 {event ids}: those events leave the map (all but this one);
//   - 20 {[row, column]}: the fleet (and the event it runs) moves there
//     (EffectEventTeleportEvent: the client then focuses on the fleet);
//   - 21 {ids}: triggers (world task complete_condition 8, phase 4 hook);
//   - 27 {story, [[option, effect], ...]}: the option picked (act_arg_1,
//     the effect) runs next;
//   - 33 {enemy}: that enemy's attachments leave the map;
//   - 35 {condition, effect, ..., default}: the first effect whose
//     condition holds runs next (difficulty, item, stage, collection,
//     task, a rectangle holding an event); others count as false;
//   - 36 {[effect, weight], ...}: one drawn by weight runs next (0: none).
//
// Unknown types (23, 25, 26, 28, 31, 39...) do nothing but are taken off.

// world_effect_data types the server applies.
const (
	worldEffectEnemy         = 1
	worldEffectStory         = 2
	worldEffectTeleport      = 3
	worldEffectAcceptTask    = 5
	worldEffectDrop          = 7
	worldEffectShipBuff      = 8
	worldEffectSpawnRelative = 10
	worldEffectRemoveEvents  = 11
	worldEffectBattle        = 12
	worldEffectGuide         = 13
	worldEffectDropTreasure  = 14
	worldEffectBlink1        = 16
	worldEffectBlink2        = 17
	worldEffectAchieveCarry  = 18
	worldEffectConsumeCarry  = 19
	worldEffectMoveFleet     = 20
	worldEffectTrigger       = 21
	worldEffectTerrain       = 23
	worldEffectTriggerReset  = 25
	worldEffectTriggerWait   = 26
	worldEffectConsumeItem   = 24
	worldEffectStoryOption   = 27
	worldEffectFleetShipHP   = 30
	worldEffectProgress      = 32
	worldEffectRemoveEnemies = 33
	worldEffectCondition     = 35
	worldEffectRandom        = 36
	worldEffectTeleportBack  = 37
	worldEffectReplace       = 39
	worldEffectDeleteTask    = 40
	worldEffectGlobalBuff    = 44
	worldEffectMapClearFlag  = 45
	worldEffectBrokenClean   = 48
	worldEffectCatSalvage    = 49
	worldEffectBossFreeCount = 50
	worldEffectTip           = 52 // a text the client shows
)

// worldMarkerDrop is the drop of effect 410121, the only effect of the
// walls, the Strongholds' allied fleets and the closed Arbiter device
// (effective_num 5: the client never runs it by standing on them): nothing.
const worldMarkerDrop = 1480001

// dropTypeWorldCollection is DROP_TYPE_WORLD_COLLECTION (world_collection_*:
// a file or record unlocked, COUNTINFO.collection_list).
const dropTypeWorldCollection = 13

// effectParams decodes an effect's effect_paramater.
func effectParams(effect worldEffect) []json.RawMessage {
	var params []json.RawMessage
	_ = json.Unmarshal(effect.Params, &params)
	return params
}

func rawUint(raw json.RawMessage) uint32 {
	var v float64
	if json.Unmarshal(raw, &v) != nil || v < 0 {
		return 0
	}
	return uint32(v)
}

func rawInt(raw json.RawMessage) int64 {
	var v float64
	_ = json.Unmarshal(raw, &v)
	return int64(v)
}

func rawInts(raw json.RawMessage) []int32 {
	var values []float64
	_ = json.Unmarshal(raw, &values)
	ints := make([]int32, len(values))
	for i, v := range values {
		ints[i] = int32(v)
	}
	return ints
}

func paramUints(params []json.RawMessage) []uint32 {
	var ids []uint32
	for _, raw := range params {
		if id := rawUint(raw); id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// event is OpReqEvent on the event at cell: its next effect runs; chosen is
// the effect a story option picked (act_arg_1).
func (op *worldOp) event(cell [2]uint32, chosen uint32) error {
	ev := op.m.cellEvent(cell)
	if ev == nil {
		return errRefused
	}
	home := op.m
	id := ev.Effects[0]
	ev.Effects = slices.Delete(slices.Clone(ev.Effects), 0, 1)
	op.mark(cell)
	op.happened.Events = append(op.happened.Events, ev.ID)
	if err := op.runEffect(ev, id, chosen); err != nil {
		return err
	}
	if len(ev.Effects) == 0 && slices.Contains(home.Attachments, ev) {
		home.removeAttachment(ev)
		if home == op.m {
			op.mark([2]uint32{ev.Row, ev.Column})
		}
	}
	return nil
}

// cellEvent is the event of cell whose next effect the client asks for
// (it only sends the cell): the one WorldMap:CheckInteractive picks, an
// event whose effect runs by itself (autoactivate) first, then one the
// fleet runs by standing on it (effective_num 1 at most: a wall's or a
// marker's 410121 needs 5), then any; nil for none.
func (m *worldMap) cellEvent(cell [2]uint32) *worldAttachment {
	var auto, interactive, other *worldAttachment
	for _, a := range m.cellAttachments(cell[0], cell[1]) {
		if a.Type != worldAttachEvent || len(a.Effects) == 0 {
			continue
		}
		effect := worldEffects[a.Effects[0]]
		switch {
		case effect.AutoActivate > 0 && auto == nil:
			auto = a
		case effect.EffectiveNum <= 1 && interactive == nil:
			interactive = a
		case other == nil:
			other = a
		}
	}
	for _, a := range []*worldAttachment{auto, interactive, other} {
		if a != nil {
			return a
		}
	}
	return nil
}

// runEffect applies effect id of event ev.
func (op *worldOp) runEffect(ev *worldAttachment, id, chosen uint32) error {
	effect, ok := worldEffects[id]
	if !ok {
		return nil
	}
	op.happened.Effects = append(op.happened.Effects, id)
	params := effectParams(effect)
	state := op.state
	first := func() uint32 {
		if len(params) == 0 {
			return 0
		}
		return rawUint(params[0])
	}
	switch effect.EffectType {
	case worldEffectTeleport, worldEffectTeleportBack:
		return op.teleport(ev, params)
	case worldEffectDrop:
		op.dropAwards(params)
	case worldEffectDropTreasure:
		state.Count.Treasure++
		op.dropAwards(params)
	case worldEffectShipBuff:
		if op.fleet != nil {
			for i := range op.fleet.Ships {
				op.fleet.Ships[i].Buffs = addWorldBuff(op.fleet.Ships[i].Buffs, first(), 1)
				op.markShip(op.fleet.Ships[i].ID)
			}
		}
	case worldEffectSpawnEvents, worldEffectSpawnRelative:
		op.spawnEvents(ev, paramUints(params), effect.EffectType == worldEffectSpawnRelative)
	case worldEffectRemoveEvents:
		ids := paramUints(params)
		for _, a := range slices.Clone(op.m.Attachments) {
			if a != ev && a.Type == worldAttachEvent && slices.Contains(ids, a.ID) {
				op.m.removeAttachment(a)
				op.mark([2]uint32{a.Row, a.Column})
			}
		}
	case worldEffectEnemy, worldEffectBattle:
		enemy := first()
		if _, known := expeditionTemplates[enemy]; known && op.m.stageEnemy([2]uint32{ev.Row, ev.Column}) == nil {
			if old := op.m.attachment(ev.Row, ev.Column, worldAttachEnemy, enemy); old != nil {
				op.m.removeAttachment(old)
			}
			op.m.addAttachment(newWorldEnemy(enemy, ev.Row, ev.Column))
			op.mark([2]uint32{ev.Row, ev.Column})
		}
	case worldEffectRemoveEnemies:
		ids := paramUints(params)
		for _, a := range slices.Clone(op.m.Attachments) {
			if (a.Type == worldAttachEnemy || a.Type == worldAttachBoss || a.Type == worldAttachEnemyAI) && slices.Contains(ids, a.ID) {
				op.m.removeAttachment(a)
				op.mark([2]uint32{a.Row, a.Column})
			}
		}
	case worldEffectBlink1, worldEffectBlink2, worldEffectMoveFleet:
		if len(params) == 0 {
			return nil
		}
		to := rawInts(params[0])
		if len(to) < 2 || to[0] < 0 || to[1] < 0 || !worldTemplates[op.m.TemplateID].hasCell(uint32(to[0]), uint32(to[1])) {
			return nil
		}
		cell := [2]uint32{uint32(to[0]), uint32(to[1])}
		op.mark([2]uint32{ev.Row, ev.Column}, cell)
		ev.Row, ev.Column = cell[0], cell[1]
		if effect.EffectType == worldEffectMoveFleet && op.fleet != nil && !op.fleet.Submarine {
			op.mark(fleetCell(op.fleet))
			op.fleet.Row, op.fleet.Column = cell[0], cell[1]
			discoverAround(op.m, cell[0], cell[1])
		}
	case worldEffectAchieveCarry:
		if op.fleet != nil {
			op.fleet.Carries = append(op.fleet.Carries, paramUints(params)...)
		}
	case worldEffectConsumeCarry:
		if op.fleet != nil && len(params) > 0 {
			for _, carry := range rawInts(params[0]) {
				if i := slices.Index(op.fleet.Carries, uint32(carry)); i >= 0 {
					op.fleet.Carries = slices.Delete(op.fleet.Carries, i, i+1)
				}
			}
		}
	case worldEffectConsumeItem:
		if len(params) >= 2 {
			item, count := rawUint(params[0]), rawUint(params[1])
			if state.item(item) < count {
				return errRefused
			}
			state.addItem(item, -int64(count))
		}
	case worldEffectStoryOption:
		if len(params) >= 2 && chosen != 0 {
			var options [][]uint32
			_ = json.Unmarshal(params[1], &options)
			if slices.ContainsFunc(options, func(option []uint32) bool { return len(option) == 2 && option[1] == chosen }) {
				ev.Effects = append([]uint32{chosen}, ev.Effects...)
			}
		}
	case worldEffectCondition, worldEffectRandom:
		if next := op.resolveOnce(effect, params, ev); next != 0 {
			ev.Effects = append([]uint32{next}, ev.Effects...)
		}
	case worldEffectFleetShipHP:
		if op.fleet != nil {
			delta := int64(0)
			if len(params) > 0 {
				delta = rawInt(params[0])
			}
			for i := range op.fleet.Ships {
				ship := &op.fleet.Ships[i]
				ship.HPRant = uint32(min(max(int64(ship.HPRant)+delta, 1), fullHP))
				op.markShip(ship.ID)
			}
		}
	case worldEffectProgress:
		state.Count.Progress = max(state.Count.Progress, first())
	case worldEffectDeleteTask:
		ids := paramUints(params)
		state.Tasks = slices.DeleteFunc(state.Tasks, func(task worldTask) bool { return slices.Contains(ids, task.ID) })
		op.happened.Deleted = append(op.happened.Deleted, ids...)
	case worldEffectGlobalBuff:
		if len(params) >= 2 {
			state.Buffs = addWorldBuff(state.Buffs, rawUint(params[0]), rawInt(params[1]))
		}
	case worldEffectMapClearFlag:
		op.m.Clear = first() == 1
	case worldEffectBrokenClean:
		for _, fleet := range state.Fleets {
			for i := range fleet.Ships {
				if fleet.Ships[i].broken() {
					fleet.Ships[i].Buffs = slices.DeleteFunc(fleet.Ships[i].Buffs, func(buff worldBuff) bool { return buff.ID == worldPortSettings.brokenBuff })
					op.markShip(fleet.Ships[i].ID)
				}
			}
		}
	case worldEffectCatSalvage:
		// the client asks for it only for a fleet with a meowfficer and no
		// search under way (WorldMap:CheckEventAutoTrigger)
		if op.fleet != nil && op.fleet.Salvage.RandomID == 0 && len(op.fleet.Commanders) > 0 {
			set, err := op.salvageSet(op.fleet)
			if err != nil {
				return err
			}
			hazard := worldRandomMaps[op.m.RandomID].HazardLevel
			op.fleet.Salvage = worldSalvage{RandomID: op.m.RandomID, List: drawSalvage(op.rng, set, hazard)}
			op.salvages = append(op.salvages, op.fleet.ID)
		}
	case worldEffectBossFreeCount:
		return op.tx.addWorldBossSummonFree(1)
	case worldEffectAcceptTask:
		op.happened.Accepted = append(op.happened.Accepted, first())
	case worldEffectTrigger:
		op.happened.Triggers = append(op.happened.Triggers, paramUints(params)...)
	case worldEffectTriggerReset, worldEffectTriggerWait:
		// server-only (WorldMapAttachment has no Effect for them, the
		// client just asks for the next effect): they name type 21
		// triggers, 25 before the events that fire them are spawned or
		// fired (20280: 25 [202020], then the supplies firing 202020), 26
		// after (20261: 26 [202020]; 20281: 21 [202010] then 26 [202010];
		// 20910: 25, 21, 26 [209000]), a reset and a wait on them as far
		// as the tables tell. Both autoactivate where their event does, so
		// the client asks for them as soon as the event shows: a wait
		// refused would only stall the event. They run through, the story
		// chains they end (GWORLD104: 20260, 20261, 20280, 20281, progress
		// 20 and task 2000) going on as their other effects say.
	case worldEffectTerrain:
		if worldScripted(ev.ID) {
			op.terrain(ev, params)
		}
	case worldEffectReplace:
		if ids := paramUints(params); worldScripted(ev.ID) && len(ids) == 2 {
			op.replaceEvents(ids[0], ids[1])
		}
	case worldEffectStory, worldEffectGuide:
		// the client's: the story or guide played before the request
	default:
		if effect.EffectType < 1000 && effect.EffectType != worldEffectTip {
			logger.WithFields("World", logger.FieldValue("effect", id), logger.FieldValue("type", effect.EffectType)).Debug("effect type not applied")
		}
	}
	return nil
}

// resolveOnce picks a branch effect's next effect (35: the first condition
// that holds, else the default; 36: drawn by weight), 0 for none.
func (op *worldOp) resolveOnce(effect worldEffect, params []json.RawMessage, ev *worldAttachment) uint32 {
	switch effect.EffectType {
	case worldEffectCondition:
		for i := 0; i+1 < len(params); i += 2 {
			if op.condition(params[i], ev) {
				return rawUint(params[i+1])
			}
		}
		if len(params)%2 == 1 {
			return rawUint(params[len(params)-1])
		}
	case worldEffectRandom:
		var options [][2]uint32
		for _, raw := range params {
			var option []float64
			if json.Unmarshal(raw, &option) == nil && len(option) == 2 && option[1] > 0 {
				options = append(options, [2]uint32{uint32(option[0]), uint32(option[1])})
			}
		}
		if pick := weightedPick(func(n uint32) uint32 { return op.rng.Uint32N(n) }, options, func(o [2]uint32) uint32 { return o[1] }); pick >= 0 {
			return options[pick][0]
		}
	}
	return 0
}

// resolveBranch follows branch effects (35, 36) from id to the effect they
// end on (a box's drop).
func (op *worldOp) resolveBranch(id uint32, ev *worldAttachment) uint32 {
	for range 16 {
		effect, ok := worldEffects[id]
		if !ok || (effect.EffectType != worldEffectCondition && effect.EffectType != worldEffectRandom) {
			return id
		}
		id = op.resolveOnce(effect, effectParams(effect), ev)
	}
	return id
}

// condition tells whether a type 35 condition holds.
func (op *worldOp) condition(raw json.RawMessage, ev *worldAttachment) bool {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	var kind string
	if json.Unmarshal(parts[0], &kind) != nil {
		// [row1, column1, row2, column2, event]: an event in the rectangle
		v := rawInts(raw)
		if len(v) != 5 {
			return false
		}
		for _, a := range op.m.Attachments {
			if a.Type == worldAttachEvent && a.ID == uint32(v[4]) && int32(a.Row) >= min(v[0], v[2]) && int32(a.Row) <= max(v[0], v[2]) &&
				int32(a.Column) >= min(v[1], v[3]) && int32(a.Column) <= max(v[1], v[3]) {
				return true
			}
		}
		return false
	}
	arg := func(i int) uint32 {
		if i < len(parts) {
			return rawUint(parts[i])
		}
		return 0
	}
	switch kind {
	case "difficulty":
		d := worldTemplates[op.m.TemplateID].Difficulty
		return d >= arg(1) && d <= arg(2)
	case "item":
		return op.state.item(arg(1)) >= max(arg(2), 1)
	case "stage":
		p := op.state.Count.Progress
		return p >= arg(1) && p <= arg(2)
	case "collection":
		return slices.Contains(op.state.Count.Collections, arg(1))
	case "task":
		return slices.ContainsFunc(op.state.Tasks, func(task worldTask) bool { return task.ID == arg(1) })
	case "month":
		// the Arbiters' rotation (worldarbiter.go)
		return op.state.arbiterMonth(op.now) == arg(1)
	case "max":
		// {"max", [event ids], n}: n of those events are on the map (the
		// promoted "Pawns"' random events, the reinforcement spawners: the
		// effect after it runs instead of spawning one more)
		var ids []uint32
		if len(parts) < 3 || json.Unmarshal(parts[1], &ids) != nil {
			return false
		}
		n := uint32(0)
		for _, a := range op.m.Attachments {
			if a.Type == worldAttachEvent && slices.Contains(ids, a.ID) {
				n++
			}
		}
		return n >= max(arg(2), 1)
	}
	return false
}

// spawnEvents adds events (effect types 9 and 10): one per entry of their
// grid, at a cell of a rectangle entry (spawnCell: a free one first), the
// entries offsets from the spawning event when relative (or when the grid
// has negative entries). An id listed n times spawns n times (a map
// controller's effects list the same "Pawn" or placeholder again and
// again); one already on the cell picked isn't added twice.
func (op *worldOp) spawnEvents(ev *worldAttachment, ids []uint32, relative bool) {
	template := worldTemplates[op.m.TemplateID]
	for _, id := range ids {
		config, ok := worldEvents[id]
		if !ok {
			continue
		}
		offsets := relative || slices.ContainsFunc(config.Grid, func(entry []int32) bool {
			return slices.ContainsFunc(entry, func(v int32) bool { return v < 0 })
		})
		var groups [][][2]uint32
		for _, entry := range config.Grid {
			var cells [][2]uint32
			if offsets {
				if len(entry) == 2 {
					entry = []int32{entry[0], entry[1], entry[0], entry[1]}
				}
				if len(entry) != 4 {
					continue
				}
				for dr := min(entry[0], entry[2]); dr <= max(entry[0], entry[2]); dr++ {
					for dc := min(entry[1], entry[3]); dc <= max(entry[1], entry[3]); dc++ {
						r, c := int32(ev.Row)+dr, int32(ev.Column)+dc
						if r >= 0 && c >= 0 && template.hasCell(uint32(r), uint32(c)) {
							cells = append(cells, [2]uint32{uint32(r), uint32(c)})
						}
					}
				}
			} else {
				cells = gridCells(template, entry)
			}
			if len(cells) > 0 {
				groups = append(groups, cells)
			}
		}
		// the Strongholds' and Arbiters' structures read a grid as the
		// cells one of the event goes to (a node's two gates, one on
		// either half of its walls; one final boss among three cells;
		// one Siren Crate around a retake: the wiki's single "Retake
		// Bonus"), the counts their zones need (worldstronghold.go)
		if worldScripted(id) && !worldWall(id) && len(groups) > 1 {
			groups = [][][2]uint32{slices.Concat(groups...)}
		}
		for _, cells := range groups {
			picks := [][2]uint32{spawnCell(op.m, template, cells, op.state.Fleets, op.rng)}
			if worldWall(id) {
				picks = cells
			}
			for _, cell := range picks {
				// a rock of a structure (its offsets spawned once the map
				// is shown, after the map's content) doesn't cover what a
				// fleet must reach: a resource node, a "Pawn", a fleet
				if worldObstacleMasks[config.Obstacle]&worldObstacleArrive == 0 && op.m.occupiedBut(cell, op.state.Fleets, ev) {
					continue
				}
				if op.m.addAttachment(newWorldEvent(id, cell[0], cell[1])) {
					op.mark(cell)
				}
			}
		}
	}
}

// worldScripted tells whether an event is one of the Siren Strongholds'
// structures (6000000-6099999, worldstronghold.go) or the Arbiters' Forward
// Base and battle zones (7000000-7000999, 7030000-7039999,
// worldarbiter.go): what the server builds their zones with, whose
// server-side effects (terrain, replacements) are applied, and whose grid
// is the cells one of the event goes to (spawnEvents).
func worldScripted(id uint32) bool {
	return id >= 6000000 && id < 6100000 || id >= 7000000 && id < 7001000 || id >= 7030000 && id < 7040000
}

// terrain is effect type 23 ({[[row], [column], type, args...], ...},
// offsets from the event): the cells' terrain becomes type, 0 clearing it
// (the Arbiters' Siren fog lifted around the summoning box, the currents,
// fog or poison a Stronghold device keeps until it is scanned), sent in
// land_list. The args are a template terrain's (templateLands): a stream's
// direction, a wind's and its strength, a fog's or poison's strength; a
// fog the effect gives none is taken as strength 1, an ordinary fog (the
// Siren fog, 0, can't be entered: the device under it must be reached).
func (op *worldOp) terrain(ev *worldAttachment, params []json.RawMessage) {
	template := worldTemplates[op.m.TemplateID]
	for _, raw := range params {
		var entry []json.RawMessage
		if json.Unmarshal(raw, &entry) != nil || len(entry) < 3 {
			continue
		}
		kind := rawUint(entry[2])
		arg := func(i int) int32 {
			if 3+i < len(entry) {
				return int32(rawInt(entry[3+i]))
			}
			return 0
		}
		for _, dr := range rawInts(entry[0]) {
			for _, dc := range rawInts(entry[1]) {
				r, c := int32(ev.Row)+dr, int32(ev.Column)+dc
				if r < 0 || c < 0 || !template.hasCell(uint32(r), uint32(c)) {
					continue
				}
				cell := [2]uint32{uint32(r), uint32(c)}
				op.m.Lands = slices.DeleteFunc(op.m.Lands, func(land worldLand) bool { return land.Row == cell[0] && land.Column == cell[1] })
				if kind != 0 {
					land := worldLand{Row: cell[0], Column: cell[1], Type: kind}
					switch kind {
					case worldTerrainStream:
						land.DirRow, land.DirColumn = arg(0), arg(1)
					case worldTerrainWind:
						land.DirRow, land.DirColumn, land.Distance = arg(0), arg(1), uint32(max(arg(2), 0))
					case worldTerrainFog, worldTerrainPoison:
						land.Distance = uint32(max(arg(0), 0))
						if kind == worldTerrainFog && len(entry) == 3 {
							land.Distance = 1
						}
					}
					op.m.Lands = append(op.m.Lands, land)
				}
				if !slices.Contains(op.lands, cell) {
					op.lands = append(op.lands, cell)
				}
			}
		}
	}
}

// replaceEvents is effect type 39 {from, to}: every event from on the map
// becomes event to on its cell, its effects to run (a node's walls broken
// once it is captured, the Strongholds' node counter, a device swapped).
func (op *worldOp) replaceEvents(from, to uint32) {
	if _, ok := worldEvents[to]; !ok {
		return
	}
	for _, a := range slices.Clone(op.m.Attachments) {
		if a.Type != worldAttachEvent || a.ID != from {
			continue
		}
		op.m.removeAttachment(a)
		op.m.addAttachment(newWorldEvent(to, a.Row, a.Column))
		op.mark([2]uint32{a.Row, a.Column})
	}
}

// dropAwards is EffectEventDrop's / DropTreasure's drop: its drop id
// (params[0]) is a server-side drop table, read by worldEventLoot (the
// branch it ends and the map's difficulty), a record file's
// (worldCollectionDrops) a world collection unlock.
func (op *worldOp) dropAwards(params []json.RawMessage) {
	if len(params) == 0 {
		return
	}
	drop := rawUint(params[0])
	if file, ok := worldCollectionDrops[drop]; ok {
		op.drops = append(op.drops, dropInfo(dropTypeWorldCollection, file, 1))
		return
	}
	if drop == worldMarkerDrop {
		return
	}
	difficulty := worldTemplates[op.m.TemplateID].Difficulty
	op.drops = append(op.drops, worldEventLoot(op.rng, drop, difficulty, worldZones[op.m.RandomID])...)
}

// teleport is EffectEventTeleport / TeleportBack ({[map, [row, column],
// flag]}, or {[map, entrance, [row, column], flag]}: a Siren Stronghold's
// way out to Liverpool, 2 through entrance 2): the fleets go to that map
// (row -1: its start cells) through the entrance given, else the same
// entrance, or the one the map is the base of; map 0 (TeleportBack) is
// the active entrance's base map.
func (op *worldOp) teleport(ev *worldAttachment, params []json.RawMessage) error {
	if len(params) == 0 {
		return nil
	}
	var target []json.RawMessage
	if json.Unmarshal(params[0], &target) != nil || len(target) == 0 {
		return nil
	}
	randomID := rawUint(target[0])
	entrance := op.state.EnterMapID
	pos := 1
	if len(target) >= 4 {
		if id := rawUint(target[1]); worldColormasks[id].World != 0 {
			entrance = id
		}
		pos = 2
	}
	if randomID == 0 {
		randomID = worldColormasks[entrance].Chapter
	}
	// a base map is entered through its own entrance
	if worldColormasks[entrance].Chapter != randomID {
		for _, id := range slices.Sorted(maps.Keys(worldColormasks)) {
			if worldColormasks[id].Chapter == randomID {
				entrance = id
				break
			}
		}
	}
	var cell *[2]uint32
	if len(target) > pos {
		if at := rawInts(target[pos]); len(at) == 2 && at[0] >= 0 && at[1] >= 0 {
			cell = &[2]uint32{uint32(at[0]), uint32(at[1])}
		}
	}
	from := op.m
	if err := op.switchMap(randomID, entrance, cell, false); err != nil {
		return err
	}
	if ev != nil && from != op.m {
		op.placeArrivalEvents(from, ev.ID/100)
	}
	return nil
}

// placeArrivalEvents puts on the map a story map's teleport led to the
// events of that map's family (worldEventFamily, map id * 100 + n) that no
// effect spawns and whose grid isn't on the story map's own template: they
// are the next part of the story, on the map the fleets arrive on. Map
// 202's 20201 sends the fleets to NY City (map 1) at progress 10, where
// 20260 and 20261 ((9, 0), a cell template 2020 lacks) start GWORLD104,
// accept "Chapter 1-2 Deja Vu" and bring progress 20 (SystemOutMap: the
// world map opens, OpTransport); 235's 23531 likewise sends them to map 8,
// 23550 at (11, 11). The tables say nothing else of where these events
// go: the map they fit and the family's teleport points at is a reading of
// the tables, nothing of it was captured.
func (op *worldOp) placeArrivalEvents(from *worldMap, family uint32) {
	if from.RandomID != family {
		return
	}
	dest := worldTemplates[op.m.TemplateID]
	if dest == nil {
		return
	}
	for _, id := range worldArrivalEvents(family, from.TemplateID) {
		if op.m.hasEvent(id) {
			continue
		}
		for _, a := range spawnWorldEvent(op.m, dest, id, op.state.Fleets, op.rng) {
			op.mark([2]uint32{a.Row, a.Column})
		}
	}
}

// worldArrivalEvents are the events of family (a story map, 200-999) that
// no effect spawns and whose grid has no cell on templateID, the family's
// own template (placeArrivalEvents).
func worldArrivalEvents(family, templateID uint32) []uint32 {
	own := worldTemplates[templateID]
	if family < 200 || family >= 1000 || own == nil {
		return nil
	}
	var ids []uint32
	for _, id := range worldEventFamily(family) {
		if worldSpawnedEvents[id] || slices.ContainsFunc(worldEvents[id].Grid, func(entry []int32) bool { return len(gridCells(own, entry)) > 0 }) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// hasEvent tells whether an event attachment id is on the map.
func (m *worldMap) hasEvent(id uint32) bool {
	return slices.ContainsFunc(m.Attachments, func(a *worldAttachment) bool { return a.Type == worldAttachEvent && a.ID == id })
}

// eventEffects are the effects of event id of type kind, in order.
func eventEffects(id uint32, kinds ...uint32) []worldEffect {
	var effects []worldEffect
	for _, effectID := range worldEvents[id].Effect {
		if effect, ok := worldEffects[effectID]; ok && slices.Contains(kinds, effect.EffectType) {
			effects = append(effects, effect)
		}
	}
	return effects
}

// eventProgress is the highest progress event id's effects set (32), 0
// for none.
func eventProgress(id uint32) uint32 {
	progress := uint32(0)
	for _, effect := range eventEffects(id, worldEffectProgress) {
		for _, v := range paramUints(effectParams(effect)) {
			progress = max(progress, v)
		}
	}
	return progress
}

// eventDescendants are the events id spawns (9, 10, 39), theirs, and so on.
func eventDescendants(id uint32) map[uint32]bool {
	seen := map[uint32]bool{}
	queue := []uint32{id}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, effect := range eventEffects(next, worldEffectSpawnEvents, worldEffectSpawnRelative, worldEffectReplace) {
			for _, child := range paramUints(effectParams(effect)) {
				if _, ok := worldEvents[child]; ok && !seen[child] {
					seen[child] = true
					queue = append(queue, child)
				}
			}
		}
	}
	return seen
}

// repairArrivalEvents puts back on m the arrival events a teleport of
// before placeArrivalEvents never placed (worlds stuck in NY City at
// progress 10, with nothing to bring progress 20 and open the world map).
// For each story family whose events teleport to m (effect 3 to m's random
// id), its arrival events go back when the progress is at least what the
// teleporting event sets and below what the arrival events set, and none
// of them nor of the events they spawn is on m (the chain not started, or
// already done). Only arrival events that set progress are concerned.
func repairArrivalEvents(m *worldMap, progress uint32, fleets []*worldFleet, rng *rand.Rand) bool {
	dest := worldTemplates[m.TemplateID]
	if dest == nil {
		return false
	}
	changed := false
	for _, family := range worldTeleportFamilies(m.RandomID) {
		random, ok := worldRandomMaps[family.random]
		if !ok || len(random.Templates) == 0 {
			continue
		}
		arrivals := worldArrivalEvents(family.random, random.Templates[0][0])
		target := uint32(0)
		chain := map[uint32]bool{}
		for _, id := range arrivals {
			target = max(target, eventProgress(id))
			chain[id] = true
			for child := range eventDescendants(id) {
				target = max(target, eventProgress(child))
				chain[child] = true
			}
		}
		if target == 0 || progress < family.progress || progress >= target ||
			slices.ContainsFunc(m.Attachments, func(a *worldAttachment) bool { return a.Type == worldAttachEvent && chain[a.ID] }) {
			continue
		}
		for _, id := range arrivals {
			if len(spawnWorldEvent(m, dest, id, fleets, rng)) > 0 {
				changed = true
			}
		}
	}
	return changed
}

// worldTeleportFamily is a story map one of whose events teleports to
// another map, and the progress that event sets.
type worldTeleportFamily struct {
	random, progress uint32
}

// worldTeleportFamilies are the story maps whose events teleport to
// randomID (effect 3 with an explicit map).
func worldTeleportFamilies(randomID uint32) []worldTeleportFamily {
	return worldTeleportsTo()[randomID]
}

// worldTeleportsTo is worldTeleportFamilies by destination, read once
// from the tables.
var worldTeleportsTo = sync.OnceValue(func() map[uint32][]worldTeleportFamily {
	families := map[uint32][]worldTeleportFamily{}
	for family := uint32(200); family < 1000; family++ {
		for _, id := range worldEventFamily(family) {
			for _, effect := range eventEffects(id, worldEffectTeleport) {
				params := effectParams(effect)
				var target []json.RawMessage
				if len(params) == 0 || json.Unmarshal(params[0], &target) != nil || len(target) == 0 || rawUint(target[0]) == 0 {
					continue
				}
				dest := rawUint(target[0])
				families[dest] = append(families[dest], worldTeleportFamily{random: family, progress: eventProgress(id)})
			}
		}
	}
	return families
})
