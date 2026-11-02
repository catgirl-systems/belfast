package game

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ggmolly/belfast/internal/consts"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// AdminError is a back-office edit the game's rules refuse, with the
// message the operator sees; Missing when what it is about doesn't exist.
type AdminError struct {
	Msg     string
	Missing bool
}

func (err AdminError) Error() string { return err.Msg }

func adminRefused(format string, args ...any) error {
	return AdminError{Msg: fmt.Sprintf(format, args...)}
}

func adminMissing(format string, args ...any) error {
	return AdminError{Msg: fmt.Sprintf(format, args...), Missing: true}
}

// AdminRefusal tells whether err only means a back-office edit was turned
// down: an AdminError, or the refusal of a request's helper the edit runs
// (not enough of something...), which is the operator's mistake as well.
// Its message is err's whole text.
func AdminRefusal(err error) (AdminError, bool) {
	var known AdminError
	if errors.As(err, &known) {
		return AdminError{Msg: err.Error(), Missing: known.Missing}, true
	}
	if refused(err) {
		return AdminError{Msg: err.Error()}, true
	}
	return AdminError{}, false
}

// OnlineCommander is a commander playing, as the admin back-office lists it.
type OnlineCommander struct {
	ID uint32
	IP string
}

// Online lists the commanders playing (see connect).
func (game *Game) Online() []OnlineCommander {
	game.online.mu.Lock()
	defer game.online.mu.Unlock()
	list := make([]OnlineCommander, 0, len(game.online.clients))
	for id, client := range game.online.clients {
		list = append(list, OnlineCommander{ID: id, IP: client.IP.String()})
	}
	return list
}

// IsOnline tells whether a commander is playing.
func (game *Game) IsOnline(id uint32) bool {
	return game.isOnline(id)
}

// kickTimeout is how long Kick waits for the connection to close.
const kickTimeout = 5 * time.Second

// Kick disconnects a commander playing (SC_10999, login data expired: the
// client goes back to the title screen) and waits for its connection to
// close, so no request of its runs after. It tells whether it was online.
func (game *Game) Kick(id uint32) (bool, error) {
	client := game.onlineClient(id)
	if client == nil {
		return false, nil
	}
	closed := make(chan struct{})
	client.OnClose(func() { close(closed) })
	if err := client.Kick(consts.DR_LOGIN_DATA_EXPIRED); err != nil {
		client.Close()
	}
	select {
	case <-closed:
		return true, nil
	case <-time.After(kickTimeout):
		return true, fmt.Errorf("commander %d: connection not closed after %s", id, kickTimeout)
	}
}

// Grant stores drops for a commander as grantDrops does for a request,
// recording the task progress they cause (see AdminWrite).
func (game *Game) Grant(ctx context.Context, id uint32, drops []*protobuf.DROPINFO) (bool, error) {
	for _, drop := range drops {
		kind, item, number := drop.GetType(), drop.GetId(), drop.GetNumber()
		if number == 0 || !mailStorable(kind, item) {
			return false, adminRefused("drop %d:%d:%d can't be stored", kind, item, number)
		}
	}
	return game.AdminWrite(ctx, id, func(tx *Tx) error { return tx.grantDrops(drops) })
}

// Storable tells whether Grant can store a drop (mailStorable).
func Storable(kind, id uint32) bool {
	return mailStorable(kind, id)
}

// GrantWithinBags is Grant refusing drops the dock or the gear depot can't
// hold, as a mail's claim does (bagsHold).
func (game *Game) GrantWithinBags(ctx context.Context, id uint32, drops []*protobuf.DROPINFO) (bool, error) {
	for _, drop := range drops {
		kind, item, number := drop.GetType(), drop.GetId(), drop.GetNumber()
		if number == 0 || !mailStorable(kind, item) {
			return false, adminRefused("drop %d:%d:%d can't be stored", kind, item, number)
		}
	}
	return game.AdminWrite(ctx, id, func(tx *Tx) error {
		commander, err := tx.GetCommander(ctx, id)
		if err != nil {
			return err
		}
		if err := tx.bagsHold(commander, drops); err != nil {
			if errors.Is(err, errRefused) {
				return adminRefused("the dock or the gear depot can't hold them: raise its capacity first")
			}
			return err
		}
		return tx.grantDrops(drops)
	})
}

// The bag capacities a commander starts with (the commanders table's
// defaults), the least the back-office sets.
const (
	minShipBag      = 150
	minEquipBag     = 300
	minCommanderBag = 40
)

// BagLimits is the range of each bag capacity: from what a commander starts
// with to Player.MAX_SHIP_BAG, MAX_EQUIP_BAG and MAX_COMMANDER_BAG.
func BagLimits() (ship, equip, commander [2]uint32) {
	return [2]uint32{minShipBag, maxShipBag}, [2]uint32{minEquipBag, maxEquipBag}, [2]uint32{minCommanderBag, maxCommanderBag}
}

// SetBagMax sets a commander's dock, gear depot and meowfficer bag
// capacities, each within BagLimits.
func (game *Game) SetBagMax(ctx context.Context, id, ship, equip, commander uint32) (bool, error) {
	if ship < minShipBag || ship > maxShipBag {
		return false, adminRefused("the dock holds %d to %d ships", minShipBag, maxShipBag)
	}
	if equip < minEquipBag || equip > maxEquipBag {
		return false, adminRefused("the gear depot holds %d to %d pieces", minEquipBag, maxEquipBag)
	}
	if commander < minCommanderBag || commander > maxCommanderBag {
		return false, adminRefused("the meowfficer bag holds %d to %d meowfficers", minCommanderBag, maxCommanderBag)
	}
	// a bag smaller than what it holds would leave the client's screens
	// refusing to open (the depot does), so it takes at least its contents
	return game.AdminWrite(ctx, id, func(tx *Tx) error {
		if err := tx.SetBagMax(ctx, db.SetBagMaxParams{ShipBagMax: ship, EquipBagMax: equip, CommanderBagMax: commander, ID: id}); err != nil {
			return err
		}
		if err := tx.dockHasRoom(0); err != nil {
			return adminRefused("the dock already holds more than %d ships", ship)
		}
		pieces, err := tx.CountEquipments(ctx, id)
		if err != nil {
			return err
		}
		if pieces > int64(equip) {
			return adminRefused("the gear depot already holds %d pieces", pieces)
		}
		meowfficers, err := tx.CountMeowfficers(ctx, id)
		if err != nil {
			return err
		}
		if meowfficers > int64(commander) {
			return adminRefused("the meowfficer bag already holds %d meowfficers", meowfficers)
		}
		return nil
	})
}

// NotifyMail tells a commander playing that its mailbox changed (SC_30001,
// as after a battle), so a mail sent from outside shows without a relogin.
func (game *Game) NotifyMail(ctx context.Context, id uint32) error {
	if !game.isOnline(id) {
		return nil
	}
	counts, err := mailCounts(ctx, game.DB.Queries, id)
	if err != nil {
		return err
	}
	game.deliver(nil, id, counts)
	return nil
}

// NotifyCompensations tells a commander playing that its compensations
// changed (SC_30101, as at login): CompensateProxy shows the main screen's
// button and reloads the list on entering, without a relogin.
func (game *Game) NotifyCompensations(ctx context.Context, id uint32) error {
	if !game.isOnline(id) {
		return nil
	}
	state, err := compensationState(ctx, game.DB.Queries, id, time.Now())
	if err != nil {
		return err
	}
	game.deliver(nil, id, state)
	return nil
}
