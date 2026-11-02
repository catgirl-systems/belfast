package game

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// Operation Siren (protocol module p33): the world's state, its activation,
// its maps and stamina. Built from the client's Lua and the world_* tables
// alone (no capture has the mode, the official account never reached it):
// controller/command/world/*.lua, model/proxy/worldproxy.lua,
// mod/experiment/world/model/*.lua; check on the device first.

// CS_33112 types (WorldKillCommand, the WorldMediator entry in
// controller/const/scene.lua).
const (
	worldKillRookie   = 0 // the tutorial map's battle lost: start over
	worldKillReset    = 1 // the monthly reset is due (World:CheckReset)
	worldKillProgress = 2 // the reset stage reached: the Siren zones
)

// worldLoginState is SC_33114, the world as the login burst tells it.
func worldLoginState(l *login) (proto.Message, error) {
	state, err := loadWorldState(l.ctx, l.queries, l.commander.ID, l.now)
	if err != nil {
		return nil, err
	}
	return state.baseState(), nil
}

// handleWorldState answers WorldConst.ReqWorldCheck (type 0: entering
// Operation Siren, the mail or compensation scenes, or a ship status check
// while the world is still SC_33114's base one) and ReqWorldForServer (type
// 1, after a reconnect) with the whole world (WorldProxy:NetFullUpdate). A
// world never activated has no map (map_id 0), which sends the client to
// the fleet selection.
func (game *Game) handleWorldState(client *connection.Client, req *protobuf.CS_33000) error {
	now := serverNow()
	var full *protobuf.SC_33001
	err := game.tx(context.Background(), client, func(tx *Tx) error {
		state, err := tx.worldState(now)
		if err != nil {
			return err
		}
		changed, err := tx.updateWorldPeriods(state, now)
		if err != nil {
			return err
		}
		if changed {
			if err := tx.saveWorldState(state); err != nil {
				return err
			}
		}
		full = state.fullState()
		return nil
	})
	if err != nil {
		return err
	}
	return client.Send(full)
}

// updateWorldPeriods is what time brings an activated world: the month's
// Arbiters, the Siren Strongholds due. It tells whether the world changed.
func (tx *Tx) updateWorldPeriods(state *worldState, now time.Time) (bool, error) {
	changed, err := tx.updateArbiter(state, now)
	if err != nil {
		return false, err
	}
	if state.updateStrongholds(tx.commanderID, now) {
		changed = true
	}
	return changed, nil
}

// activateWorld activates the commander's world as req asks (the map and
// entrance, the fleets, the camp), WorldActivateCommand's checks and all:
// the packet handler and the back-office both use it.
func (tx *Tx) activateWorld(req *protobuf.CS_33101, now time.Time) (*worldState, error) {
	state, err := tx.worldState(now)
	if err != nil {
		return nil, err
	}
	if state.activated() {
		return nil, errRefused
	}
	if _, ok := worldRandomMaps[req.GetId()]; !ok {
		return nil, errRefused
	}
	if _, ok := worldColormasks[req.GetEnterMapId()]; !ok {
		return nil, errRefused
	}
	fleets, err := tx.worldFleets(req.GetEliteFleetList(), state.Count.Progress)
	if err != nil {
		return nil, err
	}
	first := state.ActivateTime == 0 && state.Count.Activations == 0
	state.Camp = req.GetCamp()
	state.ActivateTime = uint32(now.Unix())
	state.ExpiredTime = uint32(nextMonth(now).Unix())
	state.Round = 0
	state.Count.Activations++
	state.MapID = req.GetId()
	state.EnterMapID = req.GetEnterMapId()
	state.Fleets = fleets
	state.DefaultFleets = nil
	for _, fleet := range fleets {
		base := worldBaseFleet{ID: fleet.ID, Commanders: fleet.Commanders}
		for _, ship := range fleet.Ships {
			base.Ships = append(base.Ships, ship.ID)
		}
		state.DefaultFleets = append(state.DefaultFleets, base)
	}
	if first {
		state.Stamina.Power = worldSettings.maxStamina
		state.Stamina.LastRecover = uint32(now.Unix())
	}
	m, err := tx.worldMap(state, state.MapID)
	if err != nil {
		return nil, err
	}
	if m == nil || worldTemplates[m.TemplateID] == nil {
		return nil, errRefused
	}
	state.enterMap(m)
	tx.placeWorldFleets(state, m)
	assignPressingAwards(state, tx.commanderID)
	tx.respawnTaskEvents(state, now)
	if _, err := tx.updateWorldPeriods(state, now); err != nil {
		return nil, err
	}
	if err := tx.saveWorldMap(m); err != nil {
		return nil, err
	}
	if err := tx.saveWorldState(state); err != nil {
		return nil, err
	}
	return state, nil
}

// handleWorldActivate answers WorldActivateCommand (CS_33101, sent by
// WorldFleetSelectMediator's OnGO when the world isn't activated): deploy
// the fleets chosen (elite_fleet_list, normal fleets then the submarine
// one) on map id through entrance enter_map_id, for the camp. A first
// activation (World:IsRookie) goes to world_default_entrance (the tutorial
// map 200 through entrance 1), the next ones, after a reset, to map 2
// (Liverpool) through entrance 2. The reply is read by
// WorldProxy:NetUpdateWorld (the client then loads the map, CS_33106);
// refusals show errorTip("world_activate_error_", result).
func (game *Game) handleWorldActivate(client *connection.Client, req *protobuf.CS_33101) error {
	ctx := context.Background()
	now := serverNow()
	var reply *protobuf.SC_33102
	err := game.tx(ctx, client, func(tx *Tx) error {
		state, err := tx.activateWorld(req, now)
		if err != nil {
			return err
		}
		reply = &protobuf.SC_33102{
			Result:          result(nil),
			World:           state.worldInfo(),
			ChapterAward:    state.chapterAwards(),
			CountInfo:       state.countInfo(),
			PortList:        state.PortMarks,
			NewFlagPortList: state.NewPortMarks,
		}
		return nil
	})
	if refused(err) {
		return client.Send(&protobuf.SC_33102{Result: result(err), CountInfo: (&worldState{}).countInfo()})
	}
	if err != nil {
		return err
	}
	return client.Send(reply)
}

// worldFleets checks the fleets of an activation and makes them: the
// commander's ships, each once, in at most as many normal fleets as the
// progress allows (World:BuildFormationIds) and one submarine fleet;
// meowfficers the commander's, each once. Fleets are numbered from 1 in
// the order sent, at full hp.
func (tx *Tx) worldFleets(list []*protobuf.ELITEFLEETINFO, progress uint32) ([]*worldFleet, error) {
	var fleets []*worldFleet
	seenShips, seenCommanders := map[uint32]bool{}, map[uint32]bool{}
	normal, submarine := 0, 0
	for _, elite := range list {
		if len(elite.GetShipIdList()) == 0 {
			continue
		}
		fleet := &worldFleet{ID: uint32(len(fleets) + 1)}
		subs := 0
		for _, id := range elite.GetShipIdList() {
			ship, err := tx.GetShip(tx.ctx, db.GetShipParams{ID: id, CommanderID: tx.commanderID})
			if errors.Is(err, sql.ErrNoRows) || seenShips[id] {
				return nil, errRefused
			}
			if err != nil {
				return nil, err
			}
			seenShips[id] = true
			if shipTeam(ship) == teamSubmarine {
				subs++
			}
			fleet.Ships = append(fleet.Ships, worldShip{ID: id, HPRant: 10000})
		}
		switch {
		case subs == len(fleet.Ships):
			fleet.Submarine = true
			submarine++
		case subs == 0:
			normal++
		default:
			return nil, errRefused
		}
		for _, commander := range elite.GetCommanders() {
			_, err := tx.GetMeowfficer(tx.ctx, db.GetMeowfficerParams{CommanderID: tx.commanderID, ID: commander.GetId()})
			if errors.Is(err, sql.ErrNoRows) || seenCommanders[commander.GetId()] {
				return nil, errRefused
			}
			if err != nil {
				return nil, err
			}
			seenCommanders[commander.GetId()] = true
			fleet.Commanders = append(fleet.Commanders, worldPos{Pos: commander.GetPos(), ID: commander.GetId()})
		}
		fleets = append(fleets, fleet)
	}
	// the submarine fleet once SystemSubmarine is open (World:BuildFormationIds)
	if normal == 0 || uint32(normal) > worldStageFleets(progress) || submarine > 1 || submarine > 0 && !worldSystemOpen(worldSystemSubmarine, progress) {
		return nil, errRefused
	}
	return fleets, nil
}

// placeWorldFleets puts the world's normal fleets on the start cells of m
// (worldStartCells) and discovers what they see; a submarine fleet has no
// cell (WorldMapFleet:Setup puts it at -1, -1).
func (tx *Tx) placeWorldFleets(state *worldState, m *worldMap) {
	var normal []*worldFleet
	for _, fleet := range state.Fleets {
		if !fleet.Submarine {
			normal = append(normal, fleet)
		}
	}
	starts := worldStartCells(m, len(normal))
	for i, fleet := range normal {
		if i >= len(starts) {
			break
		}
		fleet.Row, fleet.Column = starts[i][0], starts[i][1]
		fleet.StartRow, fleet.StartCol = fleet.Row, fleet.Column
		discoverAround(m, fleet.Row, fleet.Column)
	}
}

// handleWorldMapReq answers WorldMapReqCommand (CS_33106, WorldScene:LoadMap
// for a map the client has no content of yet) with the map (MAPINFO,
// WorldProxy:NetUpdateMap), generated the first time (worldmapgen.go). Only
// the world's maps (chapter_list) can be asked for; is_reset stays 0 (the
// "map version changed" tip).
func (game *Game) handleWorldMapReq(client *connection.Client, req *protobuf.CS_33106) error {
	ctx := context.Background()
	now := serverNow()
	var info *protobuf.MAPINFO
	err := game.tx(ctx, client, func(tx *Tx) error {
		state, err := tx.worldState(now)
		if err != nil {
			return err
		}
		if !state.activated() || state.mapTemplate(req.GetId()) == 0 {
			return errRefused
		}
		m, err := tx.worldMap(state, req.GetId())
		if err != nil {
			return err
		}
		if m == nil {
			return errRefused
		}
		var fleets []*worldFleet
		if m.RandomID == state.MapID {
			fleets = state.Fleets
		}
		info = m.mapInfo(fleets)
		return nil
	})
	if refused(err) {
		return client.Send(&protobuf.SC_33107{Result: result(err)})
	}
	if err != nil {
		return err
	}
	return client.Send(&protobuf.SC_33107{Result: result(nil), Map: info, IsReset: proto.Uint32(0)})
}

// handleWorldStaminaExchange answers WorldStaminaExchangeCommand (CS_33108
// type 1, the oil option of the stamina panel): the n-th exchange of the
// week costs world_supply_price[n] and gives world_supply_value[n] action
// points, as many exchanges a week as there are prices
// (WorldStaminaManager:GetExchangeData). The client takes the oil and adds
// the points itself (ExchangeStamina, to action_power).
func (game *Game) handleWorldStaminaExchange(client *connection.Client, req *protobuf.CS_33108) error {
	ctx := context.Background()
	now := serverNow()
	err := game.tx(ctx, client, func(tx *Tx) error {
		state, err := tx.worldState(now)
		if err != nil {
			return err
		}
		n := int(state.Stamina.Exchanges)
		if req.GetType() != 1 || !state.activated() || n >= len(worldSettings.supplyPrices) {
			return errRefused
		}
		price := worldSettings.supplyPrices[n]
		if price[0] != dropTypeResource {
			return errRefused
		}
		if err := tx.SpendResource(price[1], price[2]); err != nil {
			return err
		}
		if values := worldSettings.supplyValues[n]; len(values) > 0 {
			state.Stamina.Power += values[0]
		}
		state.Stamina.Exchanges++
		return tx.saveWorldState(state)
	})
	if err != nil && !refused(err) {
		return err
	}
	return client.Send(&protobuf.SC_33109{Result: result(err)})
}

// handleWorldKill answers WorldKillCommand and the reset checks of the
// WorldMediator entry (CS_33112):
//   - type 0, the tutorial map's battle lost (WSCommand:OpKillWorld, on a
//     world_guide_map_list map only): the world starts over, never
//     activated (the client builds a new one);
//   - type 1, the monthly reset is due (World:CheckReset(true): activated
//     and last_change_group_timestamp passed): time is the new due date
//     while it isn't, or while the progress is below world_resetting_stage
//     (the reset waits for the story's end, gametip world_reset_tip);
//     time 0 is the reset (state.reset), its drop_list the Supply Shop
//     Vouchers and items the world inventory turned into
//     (worldResetDrops), which the client adds itself
//     (PlayerConst.addTranDrop) and shows in its reset window;
//   - type 2, the reset stage reached: the Siren zones (sairen_chapter).
func (game *Game) handleWorldKill(client *connection.Client, req *protobuf.CS_33112) error {
	ctx := context.Background()
	now := serverNow()
	reply := &protobuf.SC_33113{Time: proto.Uint32(0)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		state, err := tx.worldState(now)
		if err != nil {
			return err
		}
		switch req.GetType() {
		case worldKillRookie:
			// only from a tutorial map (WorldConst.IsRookieMap): anywhere
			// else it would be a fresh world (full action points, new
			// zone rewards) for the asking
			if !state.activated() || !slices.Contains(worldOpSettings.rookieMaps, state.MapID) {
				return errRefused
			}
			return tx.clearWorld()
		case worldKillReset:
			if !state.activated() {
				return errRefused
			}
			if uint32(now.Unix()) < state.ExpiredTime {
				reply.Time = proto.Uint32(state.ExpiredTime)
				return nil
			}
			if state.Count.Progress < worldSettings.resetStage {
				state.ExpiredTime = uint32(nextMonth(now).Unix())
				reply.Time = proto.Uint32(state.ExpiredTime)
				return tx.saveWorldState(state)
			}
			drops, err := tx.worldResetDrops(state)
			if err != nil {
				return err
			}
			reply.DropList = drops
			if err := tx.DeleteWorldMaps(tx.ctx, tx.commanderID); err != nil {
				return err
			}
			return tx.saveWorldState(state.reset(now))
		case worldKillProgress:
			if state.updateStrongholds(tx.commanderID, now) {
				if err := tx.saveWorldState(state); err != nil {
					return err
				}
			}
			reply.SairenChapter = state.SairenChapters
			return nil
		}
		return errRefused
	})
	if err != nil && !refused(err) {
		return err
	}
	reply.Result = result(err)
	return client.Send(reply)
}

// reset is the world after the monthly reset (World:Dispose with inherit:
// realm, default fleets, achievements, activate count, progress, stamina
// manager and collections carry over). The wiki's Operation Siren page
// ("Reset") lists what goes: the zones and map travel (maps, secured and
// Siren zones, transports), the world inventory (worldResetDrops turned
// part of it into vouchers), the action points (set to the max, 200), the
// adaptive tuning (global buffs), the fleets' damage (the next activation
// deploys them at full hp), the unfinished daily missions (the queue and
// the dailies under way) and the uncompleted Siren Strongholds; and what
// stays: the exploration rewards and progress (targets), the unlocked
// ports, the META Showdown, the vouchers (a player resource), the
// collection, the unfinished "Investigate Siren Research" missions and the
// week's oil purchases of action points. The Siren research missions
// (server_type 2) and the collection file missions (4) stay, finished or
// not (a finished one isn't offered again); the port shops' pools start
// again (Ports), as do the new port shop's purchases (goods_list) and the
// voucher shop's (out_shop_buy_list).
func (state *worldState) reset(now time.Time) *worldState {
	next := &worldState{
		Camp:            state.Camp,
		DefaultFleets:   state.DefaultFleets,
		Targets:         state.Targets,
		TargetFetches:   state.TargetFetches,
		TaskFinishCount: state.TaskFinishCount,
		Count: worldCountInfo{
			Progress:    state.Count.Progress,
			Activations: state.Count.Activations,
			Collections: state.Count.Collections,
		},
		Stamina:     state.Stamina,
		GlobalFlags: state.GlobalFlags,
		MonthBoss:   state.MonthBoss,
		Arbiter:     state.Arbiter,
	}
	if state.Count.Progress >= worldSirenSettings.openStage {
		// the reset's Strongholds (updateStrongholds, once activated)
		next.Strongholds.Since = uint32(now.Unix())
	}
	for _, task := range state.Tasks {
		// the month's Arbiter tasks are the Arbiters' (updateArbiter)
		if server := worldTasks[task.ID].ServerType; server == worldTaskServerPort || server == worldTaskServerCollection ||
			task.ID == worldArbiterTaskNormal || task.ID == worldArbiterTaskHard {
			next.Tasks = append(next.Tasks, task)
		}
	}
	next.Stamina.Power, next.Stamina.Extra = worldSettings.maxStamina, 0
	next.Stamina.LastRecover = uint32(now.Unix())
	if len(state.Fleets) > 0 {
		next.DefaultFleets = nil
		for _, fleet := range state.Fleets {
			base := worldBaseFleet{ID: fleet.ID, Commanders: slices.Clone(fleet.Commanders)}
			for _, ship := range fleet.Ships {
				base.Ships = append(base.Ships, ship.ID)
			}
			next.DefaultFleets = append(next.DefaultFleets, base)
		}
	}
	return next
}
