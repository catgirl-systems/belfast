package game

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// Cryptolalia.COST_TYPE_* (model/vo/cryptolalia/cryptolalia.lua): which
// soundstory_template cost an unlock pays, cost1 or cost2.
const (
	cryptolaliaCostGem    = 1
	cryptolaliaCostTicket = 2
)

// cryptolaliaStory is a soundstory_template row: a Cryptolalia voice story,
// on sale during time for cost1 (gems) or cost2 (tickets), each a
// [DROP_TYPE_*, id, count].
type cryptolaliaStory struct {
	Name  string          `json:"name"`
	Cost1 []uint32        `json:"cost1"`
	Cost2 []uint32        `json:"cost2"`
	Time  gamedata.Window `json:"time"`
}

var cryptolaliaStories map[uint32]cryptolaliaStory

func loadCryptolaliaData() error {
	return gamedata.Load("soundstory_template", &cryptolaliaStories)()
}

// handleUnlockCryptolalia answers UnlockCryptolaliaCommand
// (controller/command/cryptolalia/unlockcryptolaliacommand.lua): unlocks the
// soundstory_template id, paid with its cost1 or cost2 as cost_type says.
// On ret 0 the client marks it unlocked and takes the cost off itself; the
// unlocks come back at login in SC_11003.soundstory. The client checks the
// story is locked and on sale and the cost; so does the server. No official
// capture has it: the reply is built from the Lua alone.
func (game *Game) handleUnlockCryptolalia(client *connection.Client, req *protobuf.CS_16205) error {
	err := game.tx(context.Background(), client, func(tx *Tx) error {
		return tx.unlockCryptolalia(req.GetId(), req.GetCostType(), time.Now())
	})
	if err != nil && !refused(err) {
		return err
	}
	return client.Send(&protobuf.SC_16206{Ret: result(err)})
}

// unlockCryptolalia unlocks a story on sale and not unlocked yet, paying the
// cost of costType.
func (tx *Tx) unlockCryptolalia(id, costType uint32, now time.Time) error {
	story, ok := cryptolaliaStories[id]
	if !ok || !story.Time.Open(now) {
		return errRefused
	}
	var cost []uint32
	switch costType {
	case cryptolaliaCostGem:
		cost = story.Cost1
	case cryptolaliaCostTicket:
		cost = story.Cost2
	}
	// every cost is a player_resource (cost1 the free gems, cost2 the
	// soundStoryCard); the client only takes resources off (Player:consume)
	if len(cost) != 3 || cost[0] != dropTypeResource {
		return errRefused
	}
	added, err := tx.UnlockCryptolalia(tx.ctx, db.UnlockCryptolaliaParams{CommanderID: tx.commanderID, StoryID: id})
	if err != nil {
		return err
	}
	if added == 0 {
		return errRefused
	}
	return tx.SpendShopResource(cost[1], cost[2])
}

// --- back office

// AdminCryptolalia is a soundstory_template story and whether the commander
// unlocked it.
type AdminCryptolalia struct {
	ID       uint32 `json:"id"`
	Name     string `json:"name"`
	Unlocked bool   `json:"unlocked"`
}

// AdminCryptolaliaList lists every story, in id order, the unlocked ones
// marked. The client shows an unlock (SC_11003.soundstory) whether or not
// the story is on sale (Player:GetCryptolaliaList).
func (game *Game) AdminCryptolaliaList(ctx context.Context, commanderID uint32) ([]AdminCryptolalia, error) {
	ids, err := game.DB.ListCryptolalia(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	list := []AdminCryptolalia{}
	for _, id := range slices.Sorted(maps.Keys(cryptolaliaStories)) {
		list = append(list, AdminCryptolalia{ID: id, Name: strings.TrimSpace(cryptolaliaStories[id].Name), Unlocked: slices.Contains(ids, id)})
	}
	return list, nil
}

// SetCryptolaliaUnlocked unlocks a story (every story, id 0) for free,
// whether on sale or not, or locks it again.
func (game *Game) SetCryptolaliaUnlocked(ctx context.Context, commanderID, id uint32, unlocked bool) (bool, error) {
	ids := []uint32{id}
	if id == 0 {
		if !unlocked {
			return false, AdminError{Msg: "lock the stories one by one"}
		}
		ids = slices.Sorted(maps.Keys(cryptolaliaStories))
	} else if _, ok := cryptolaliaStories[id]; !ok {
		return false, adminRefused("no cryptolalia story %d", id)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		for _, story := range ids {
			if !unlocked {
				_, err := tx.LockCryptolalia(tx.ctx, db.LockCryptolaliaParams{CommanderID: tx.commanderID, StoryID: story})
				return err
			}
			if _, err := tx.UnlockCryptolalia(tx.ctx, db.UnlockCryptolaliaParams{CommanderID: tx.commanderID, StoryID: story}); err != nil {
				return err
			}
		}
		return nil
	})
}

// UnlockAllCryptolalia unlocks every story for free.
func (game *Game) UnlockAllCryptolalia(ctx context.Context, commanderID uint32) (bool, error) {
	return game.SetCryptolaliaUnlocked(ctx, commanderID, 0, true)
}
