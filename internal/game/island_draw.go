package game

import (
	"context"
	"maps"
	"math/rand/v2"
	"slices"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// The island's outfit draw (DrawAwardActivity, activityTypeIslandDrawAward,
// IslandShopDrawAwardPage): a box of island_draw[activity].reward_list
// prizes ([island_draw_reward id, copies]) drawn without replacement.
//   - data1: the draws the commander can make. The tickets
//     (island_draw.cost_free / cost_charge, item_virtual_data_statistics
//     virtual_type 101) add to the data1 of their link_id activity
//     (Drop's case 101), the charged one bought from island_draw.shop.
//   - data2: the draws made, which unlock reward_acc at acc_count_list.
//   - data1_list: the order the S prizes (rarity 4) come out in, chosen by
//     the commander (CheckList: exactly the S copies left).
//   - data1KeyValueList[1]: the copies drawn of each prize.
//   - data2_list: the reward_acc prizes claimed; belfast keeps them as
//     the claim count of each in key-value list islandDrawClaimList (not
//     sent: the client only reads list 1) and sends them expanded.
//
// ActivityDrawAwardCommand's CS_11202:
//   - cmd 1 (set_list): arg_list is the S order.
//   - cmd 2 (do_draw): arg1 draws, number is the prizes in the order drawn
//     (ResultDraw), award_list their drops.
//   - cmd 3 (count_award): arg1 is the reward_acc prize to claim.
//
// Built from the Lua alone (no capture, the wiki has nothing on it).
// GUESSES: every copy left in the box is equally likely (a box lottery, so
// the last draws empty it; island_draw keeps no rates); an S copy drawn
// while an order is set is the order's next S; tickets are kept even once
// the activity ended; convert (the leftover conversion) isn't run.
const islandDrawClaimList = 2

// island_draw row, by activity id (DrawAwardActivity.GetDrawConfig)
type islandDrawConfig struct {
	RewardList   [][2]uint32 `json:"reward_list"`
	RewardAcc    [][2]uint32 `json:"reward_acc"`
	AccCountList []uint32    `json:"acc_count_list"`
}

// island_draw_reward row: a prize and its rarity (4 S, 3 A, 2 B, 1 C)
type islandDrawReward struct {
	DropType uint32 `json:"drop_type"`
	DropID   uint32 `json:"drop_id"`
	DropNum  uint32 `json:"drop_num"`
	Rarity   uint32 `json:"rarity"`
}

// islandDrawRaritySpecial is the rarity whose order the commander sets
const islandDrawRaritySpecial = 4

// item_virtual_data_statistics virtual_type of the draw tickets
const vitemTypeDrawTicket = 101

var islandDraw struct {
	configs map[uint32]islandDrawConfig
	rewards map[uint32]islandDrawReward
	tickets map[uint32]uint32 // ticket vitem → its draw activity
}

func loadIslandDrawData() error {
	var vitems map[uint32]struct {
		VirtualType uint32 `json:"virtual_type"`
		LinkID      uint32 `json:"link_id"`
	}
	err := gamedata.LoadAll(
		gamedata.Load("island_draw", &islandDraw.configs),
		gamedata.Load("island_draw_reward", &islandDraw.rewards),
		gamedata.Load("item_virtual_data_statistics", &vitems),
	)
	if err != nil {
		return err
	}
	islandDraw.tickets = map[uint32]uint32{}
	for id, vitem := range vitems {
		if vitem.VirtualType == vitemTypeDrawTicket {
			islandDraw.tickets[id] = vitem.LinkID
		}
	}
	return nil
}

// addDrawTickets adds count draws to a draw activity's data1, as the
// client does with a virtual_type 101 drop.
func (tx *Tx) addDrawTickets(activityID, count uint32) error {
	config, ok := activityConfigs[activityID]
	if !ok {
		return nil
	}
	state, err := activityState(tx.ctx, tx.Queries, tx.commanderID, config)
	if err != nil {
		return err
	}
	state.Data1 += count
	return tx.SaveActivity(tx.ctx, db.SaveActivityParams(state))
}

// islandDrawClaims are the reward_acc prizes the commander claimed: the
// activity's data2_list, kept as claim counts.
func islandDrawClaims(lists map[uint32][]*protobuf.KEYVALUE_P11) []uint32 {
	var claims []uint32
	for _, entry := range lists[islandDrawClaimList] {
		for range entry.GetValue() {
			claims = append(claims, entry.GetKey())
		}
	}
	return claims
}

// islandDrawOperation answers ActivityDrawAwardCommand's CS_11202 on a
// running draw activity, see above. A refused operation gets result 1.
func (game *Game) islandDrawOperation(client *connection.Client, config activityConfig, req *protobuf.CS_11202) error {
	ctx := context.Background()
	draw, ok := islandDraw.configs[config.ID]
	reply := &protobuf.SC_11203{Result: result(nil)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		if !ok {
			return errRefused
		}
		state, err := activityState(ctx, tx.Queries, client.CommanderID, config)
		if err != nil {
			return err
		}
		lists, err := activityKeyValues(ctx, tx.Queries, client.CommanderID)
		if err != nil {
			return err
		}
		drawn := map[uint32]uint32{}
		for _, entry := range lists[config.ID][virtualBagList] {
			drawn[entry.GetKey()] = entry.GetValue()
		}
		left := map[uint32]uint32{}
		for _, prize := range draw.RewardList {
			left[prize[0]] += prize[1]
		}
		for id, n := range drawn {
			left[id] -= min(n, left[id])
		}
		switch req.GetCmd() {
		case 1:
			// CheckList: the S copies left, each as often as it's left
			want := map[uint32]uint32{}
			for id, n := range left {
				if islandDraw.rewards[id].Rarity == islandDrawRaritySpecial && n > 0 {
					want[id] = n
				}
			}
			for _, id := range req.GetArgList() {
				if want[id] == 0 {
					return errRefused
				}
				want[id]--
			}
			for _, n := range want {
				if n != 0 {
					return errRefused
				}
			}
			state.Data1List = slices.Clone(req.GetArgList())
		case 2:
			count := req.GetArg1()
			total := uint32(0)
			for _, n := range left {
				total += n
			}
			if count == 0 || count > state.Data1 || count > total {
				return errRefused
			}
			order := slices.Sorted(maps.Keys(left))
			for range count {
				pick := rand.Uint32N(total)
				var id uint32
				for _, candidate := range order {
					if pick < left[candidate] {
						id = candidate
						break
					}
					pick -= left[candidate]
				}
				if islandDraw.rewards[id].Rarity == islandDrawRaritySpecial && len(state.Data1List) > 0 {
					id, state.Data1List = state.Data1List[0], state.Data1List[1:]
				}
				left[id]--
				total--
				reward := islandDraw.rewards[id]
				reply.Number = append(reply.Number, id)
				reply.AwardList = append(reply.AwardList, dropInfo(reward.DropType, reward.DropID, reward.DropNum))
				if err := tx.addActivityItem(config.ID, id, 1); err != nil {
					return err
				}
			}
			state.Data1 -= count
			state.Data2 += count
		case 3:
			// CanCountAward: the next acc_count_list step reached, and the
			// prize claimed fewer times than reward_acc gives it
			target := req.GetArg1()
			claims := islandDrawClaims(lists[config.ID])
			if len(claims) >= len(draw.AccCountList) || state.Data2 < draw.AccCountList[len(claims)] {
				return errRefused
			}
			allowed := uint32(0)
			for _, prize := range draw.RewardAcc {
				if prize[0] == target {
					allowed += prize[1]
				}
			}
			if uint32(len(slices.DeleteFunc(claims, func(id uint32) bool { return id != target }))) >= allowed {
				return errRefused
			}
			reward := islandDraw.rewards[target]
			reply.AwardList = []*protobuf.DROPINFO{dropInfo(reward.DropType, reward.DropID, reward.DropNum)}
			if err := tx.AddActivityKeyValue(ctx, db.AddActivityKeyValueParams{
				CommanderID: client.CommanderID, ActivityID: config.ID, ListKey: islandDrawClaimList, ItemKey: target, Value: 1,
			}); err != nil {
				return err
			}
		default:
			return errRefused
		}
		if err := tx.grantDrops(reply.AwardList); err != nil {
			return err
		}
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
