package game

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// The island map of the sixth anniversary (SixthAnniversaryIslandProxy,
// SixthAnniversaryIslandScene, ACTIVITY_TYPE_ISLAND): nodes
// (activity_map_event_list) each at an event (activity_map_event_data),
// completed once at event 0. The client works out itself which nodes are
// unlocked or shown (IslandNode:GetUnlock, IsVisual) from every node's
// state, so the server lists all of them. The events are played through
// CS_11202 (IslandEventTriggerCommand cmd 1, IslandNodeMarkCommand cmd 2,
// IslandFlowerFieldGetAwardCommand cmd 1 on the flower field activity),
// see islandOperation and flowerFieldOperation.

// ActivityConst.ACTIVITY_TYPE_ISLAND
const activityTypeIsland = 95

// activity_map_event_list row: a node, the event it starts at and whether
// it starts over every day (refresh)
type islandNodeConfig struct {
	EventID uint32 `json:"event_id"`
	Refresh uint32 `json:"refresh"`
}

var islandNodeConfigs map[uint32]islandNodeConfig

func loadIslandData() error {
	return gamedata.LoadAll(
		gamedata.Load("activity_map_event_list", &islandNodeConfigs),
		gamedata.Load("activity_map_event_data", &islandEventConfigs),
		loadIslandDrawData,
	)
}

// handleIslandNodes answers IslandRequestCommand (CS_26108, sent when the
// island opens and again once the server day changed,
// SixthAnniversaryIslandProxy:CheckValid): every node with its event and
// whether it is new (IslandNode). Built from the Lua alone (no capture);
// GUESSES: a node the commander never reached is at its event_id and new,
// and the refresh nodes go back to their event_id, new, at the first
// request of each server day. An island activity that isn't running gets
// ret 1 (the reply's result field is ret).
func (game *Game) handleIslandNodes(client *connection.Client, req *protobuf.CS_26108) error {
	ctx := context.Background()
	now := time.Now()
	config, open, err := game.commanderActivity(ctx, client.CommanderID, req.GetActId(), now)
	if err != nil {
		return err
	}
	if !open || config.Type != activityTypeIsland {
		return client.Send(&protobuf.SC_26109{Ret: result(errRefused)})
	}
	reply := &protobuf.SC_26109{Ret: result(nil)}
	err = game.tx(ctx, client, func(tx *Tx) error {
		rows, err := tx.ListIslandNodes(ctx, db.ListIslandNodesParams{CommanderID: client.CommanderID, ActivityID: config.ID})
		if err != nil {
			return err
		}
		stored := make(map[uint32]db.CommanderIslandNode, len(rows))
		for _, row := range rows {
			stored[row.NodeID] = row
		}
		for _, id := range slices.Sorted(maps.Keys(islandNodeConfigs)) {
			node, ok := stored[id]
			nodeConfig := islandNodeConfigs[id]
			if !ok || (nodeConfig.Refresh == 1 && !sameServerDay(time.Unix(node.RefreshedAt, 0), now)) {
				node = db.CommanderIslandNode{
					CommanderID: client.CommanderID, ActivityID: config.ID, NodeID: id,
					EventID: nodeConfig.EventID, IsNew: true, RefreshedAt: now.Unix(),
				}
				if err := tx.SaveIslandNode(ctx, db.SaveIslandNodeParams(node)); err != nil {
					return err
				}
			}
			isNew := uint32(0)
			if node.IsNew {
				isNew = 1
			}
			reply.NodeList = append(reply.NodeList, &protobuf.PB_ISLAND_NODE{
				Id: proto.Uint32(id), EventId: proto.Uint32(node.EventID), IsNew: proto.Uint32(isNew),
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return client.Send(reply)
}

// ActivityConst.ACTIVITY_TYPE_FLOWER_FIELD: the island's flower field
// (IslandFlowerFieldLayer), config_data [node, [type, id, count]]
const activityTypeFlowerField = 98

// activity_map_event_data row: type 1 a story, 2 a story with options
// (params [[op, [type, id, count] cost?], ...], IslandEvent.CheckTrigger),
// 3 a story then a QTE game, 4 a treasure.
type islandEventConfig struct {
	Type   uint32          `json:"type"`
	Params json.RawMessage `json:"params"`
}

const islandEventChoice = 2

var islandEventConfigs map[uint32]islandEventConfig

// islandEventCost is what option op of a type 2 event costs (nil when it
// is free), false when the event has no such option.
func islandEventCost(event islandEventConfig, op uint32) ([]uint32, bool) {
	var options [][]json.RawMessage
	if json.Unmarshal(event.Params, &options) != nil {
		return nil, false
	}
	for _, option := range options {
		var id uint32
		if len(option) == 0 || json.Unmarshal(option[0], &id) != nil || id != op {
			continue
		}
		var cost []uint32
		if len(option) > 1 && json.Unmarshal(option[1], &cost) == nil && len(cost) == 3 {
			return cost, true
		}
		return nil, true
	}
	return nil, false
}

// islandNode is the commander's state of a node, as handleIslandNodes
// lists it: its event_id and new until it was reached.
func (tx *Tx) islandNode(activityID, nodeID uint32, now time.Time) (db.CommanderIslandNode, error) {
	rows, err := tx.ListIslandNodes(tx.ctx, db.ListIslandNodesParams{CommanderID: tx.commanderID, ActivityID: activityID})
	if err != nil {
		return db.CommanderIslandNode{}, err
	}
	for _, row := range rows {
		if row.NodeID == nodeID {
			return row, nil
		}
	}
	return db.CommanderIslandNode{
		CommanderID: tx.commanderID, ActivityID: activityID, NodeID: nodeID,
		EventID: islandNodeConfigs[nodeID].EventID, IsNew: true, RefreshedAt: now.Unix(),
	}, nil
}

// islandOperation answers the island's CS_11202 (SixthAnniversaryIsland):
//   - cmd 1 (IslandEventTriggerCommand): node arg1 played its event with
//     option arg2 (the story's choice, or the QTE's result). A type 2
//     event's option takes its cost, which the client also takes
//     (AfterTrigger). number[1] is the node's next event, 0 when done.
//   - cmd 2 (IslandNodeMarkCommand): node arg1 isn't new anymore.
//
// GUESSES (no capture, nothing in the tables or on the wiki): every event
// ends the node (next event 0) and gives no award; the follow-up events
// (2207-2218, 2302, 2402, 2404) and the events' rewards are only known to
// the official server.
func (game *Game) islandOperation(client *connection.Client, config activityConfig, req *protobuf.CS_11202) error {
	ctx := context.Background()
	now := time.Now()
	reply := &protobuf.SC_11203{Result: result(nil)}
	nodeID := req.GetArg1()
	err := game.tx(ctx, client, func(tx *Tx) error {
		if _, ok := islandNodeConfigs[nodeID]; !ok {
			return errRefused
		}
		node, err := tx.islandNode(config.ID, nodeID, now)
		if err != nil {
			return err
		}
		switch req.GetCmd() {
		case 1:
			event, ok := islandEventConfigs[node.EventID]
			if node.EventID == 0 || !ok {
				return errRefused
			}
			if event.Type == islandEventChoice {
				cost, ok := islandEventCost(event, req.GetArg2())
				if !ok {
					return errRefused
				}
				if cost != nil {
					if err := tx.spendIslandEventCost(cost); err != nil {
						return err
					}
				}
			}
			node.EventID = 0
			reply.Number = []uint32{node.EventID}
		case 2:
			node.IsNew = false
		default:
			return errRefused
		}
		return tx.SaveIslandNode(ctx, db.SaveIslandNodeParams(node))
	})
	if refused(err) {
		return client.Send(&protobuf.SC_11203{Result: result(err)})
	}
	if err != nil {
		return err
	}
	return client.Send(reply)
}

// spendIslandEventCost takes an event option's [type, id, count]: a
// resource, an item or an activity's own item (IslandEvent.AfterTrigger).
func (tx *Tx) spendIslandEventCost(cost []uint32) error {
	kind, id, count := cost[0], cost[1], cost[2]
	switch {
	case kind == dropTypeResource:
		return tx.SpendResource(id, count)
	case kind == dropTypeItem:
		return tx.SpendItem(id, count)
	}
	if activityID, ok := activityItemDrop(kind); ok {
		return tx.spendActivityItem(activityID, id, count)
	}
	return errRefused
}

// flowerFieldOperation answers IslandFlowerFieldGetAwardCommand (CS_11202
// cmd 1): the flower field's harvest, once per server day
// (IslandFlowerFieldLayer: ready from the day after max(data1, data2)).
// number is the new data1 and data2. The harvest is config_data's drop.
// GUESSES: the harvest is that drop for each server day since the last
// one (the auto claim the layer makes after a day away shows them as
// collected while away), once for the first; data1 is the claim time and
// data2 the one before.
func (game *Game) flowerFieldOperation(client *connection.Client, config activityConfig, req *protobuf.CS_11202) error {
	ctx := context.Background()
	now := time.Now()
	var data []json.RawMessage
	var drop []uint32
	if req.GetCmd() != 1 || json.Unmarshal(config.ConfigData, &data) != nil || len(data) < 2 ||
		json.Unmarshal(data[1], &drop) != nil || len(drop) != 3 {
		return client.Send(&protobuf.SC_11203{Result: result(errRefused)})
	}
	reply := &protobuf.SC_11203{Result: result(nil)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		state, err := activityState(ctx, tx.Queries, client.CommanderID, config)
		if err != nil {
			return err
		}
		days := uint32(1)
		if last := max(state.Data1, state.Data2); last != 0 {
			elapsed := startOfDay(now).Sub(startOfDay(time.Unix(int64(last), 0))).Hours() / 24
			if elapsed < 1 {
				return errRefused
			}
			days = uint32(elapsed)
		}
		reply.AwardList = configDrops([][]uint32{{drop[0], drop[1], drop[2] * days}})
		if err := tx.grantDrops(reply.AwardList); err != nil {
			return err
		}
		state.Data2, state.Data1 = state.Data1, uint32(now.Unix())
		reply.Number = []uint32{state.Data1, state.Data2}
		return tx.SaveActivity(ctx, db.SaveActivityParams(state))
	})
	if refused(err) {
		return client.Send(&protobuf.SC_11203{Result: result(errRefused)})
	}
	if err != nil {
		return err
	}
	return client.Send(reply)
}
