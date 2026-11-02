package game

import (
	"context"
	"slices"

	"github.com/ggmolly/belfast/internal/db"
)

// MaxShipStrength enhances a ship's attributes (ids 2 to 6 of
// shipModAttrs, all of them when attrs is empty) to the exp a real
// enhancement stops at (handleModShip: its top limit times its exp ratio).
func (game *Game) MaxShipStrength(ctx context.Context, commanderID, shipID uint32, attrs []uint32) (bool, error) {
	if _, err := adminShip(ctx, game.DB.Queries, commanderID, shipID); err != nil {
		return false, err
	}
	if err := checkModAttrs(attrs); err != nil {
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
		if _, ok := strengthens[ownableShips[ship.TemplateID].StrengthenID]; !ok || specialShip(ship.TemplateID) {
			return adminRefused("the ship can't be enhanced")
		}
		return tx.maxShipStrength(ship, attrs)
	})
}

// MaxDockStrength enhances every ship of the dock that can be as
// MaxShipStrength does, those in the chapter being played left alone.
func (game *Game) MaxDockStrength(ctx context.Context, commanderID uint32, attrs []uint32) (bool, error) {
	if err := checkModAttrs(attrs); err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		ships, err := tx.ListShipsByCommander(ctx, commanderID)
		if err != nil {
			return err
		}
		inChapter, err := shipsInChapter(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		for _, ship := range ships {
			if inChapter[ship.ID] || specialShip(ship.TemplateID) {
				continue
			}
			if err := tx.maxShipStrength(ship, attrs); err != nil {
				return err
			}
		}
		return nil
	})
}

func checkModAttrs(attrs []uint32) error {
	for _, id := range attrs {
		if !slices.ContainsFunc(shipModAttrs, func(attr struct {
			id   uint32
			name string
		}) bool {
			return attr.id == id
		}) {
			return adminRefused("no enhanceable attribute %d", id)
		}
	}
	return nil
}

func (tx *Tx) maxShipStrength(ship db.Ship, attrs []uint32) error {
	strengthen, ok := strengthens[ownableShips[ship.TemplateID].StrengthenID]
	if !ok {
		return nil
	}
	rows, err := tx.ListShipStrengths(tx.ctx, ship.ID)
	if err != nil {
		return err
	}
	current := map[uint32]uint32{}
	for _, row := range rows {
		current[row.AttrID] = row.Exp
	}
	for i, attr := range shipModAttrs {
		if len(attrs) != 0 && !slices.Contains(attrs, attr.id) {
			continue
		}
		limit := shipModTopLimit(ship, strengthen, i)
		if limit == 0 {
			continue
		}
		exp := limit * shipModExpRatio(ship, strengthen, i)
		if exp == current[attr.id] {
			continue
		}
		if err := tx.SetShipStrength(tx.ctx, db.SetShipStrengthParams{ShipID: ship.ID, AttrID: attr.id, Exp: exp}); err != nil {
			return err
		}
	}
	return nil
}

// EditDockGear puts depot equipment in the slots of several ships (ship:
// slot: equipment, 0 empties a slot) in one write, each ship as EditShip's
// gear does.
func (game *Game) EditDockGear(ctx context.Context, commanderID uint32, gear map[uint32]map[uint32]uint32) (bool, error) {
	for shipID := range gear {
		if _, err := adminShip(ctx, game.DB.Queries, commanderID, shipID); err != nil {
			return false, err
		}
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		inChapter, err := shipsInChapter(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		ids := make([]uint32, 0, len(gear))
		for id := range gear {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		ships := make([]db.Ship, len(ids))
		for i, id := range ids {
			if ships[i], err = adminShip(ctx, tx.Queries, commanderID, id); err != nil {
				return err
			}
			if inChapter[id] {
				return adminRefused("ship %d is in the chapter being played", id)
			}
		}
		// the slots that change are emptied first, so a piece can move
		// from one ship to another
		for i, ship := range ships {
			empty := map[uint32]uint32{}
			for pos := range gear[ids[i]] {
				empty[pos] = 0
			}
			if err := tx.editShipGear(ship, empty); err != nil {
				return err
			}
		}
		for i, ship := range ships {
			if err := tx.editShipGear(ship, gear[ids[i]]); err != nil {
				return err
			}
		}
		return nil
	})
}
