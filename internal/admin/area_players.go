package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/game"
)

// registerPlayers registers the API of a commander's profile, items, gear, wardrobe and mail.
func (server *Server) registerPlayers(api apiFunc) {
	api("GET /api/commanders/{id}/profile", server.handlePlayerProfile)
	api("POST /api/commanders/{id}/rename", server.handlePlayerRename)
	api("POST /api/commanders/{id}/level", server.handlePlayerLevel)
	api("DELETE /api/commanders/{id}/secondary-password", server.handlePlayerResetPassword)
	api("GET /api/commanders/{id}/export", server.handlePlayerExport)
	api("DELETE /api/commanders/{id}", server.handlePlayerDelete)

	api("GET /api/commanders/{id}/items", server.handlePlayerItems)
	api("PUT /api/commanders/{id}/items/{item}", server.handlePlayerSetItem)
	api("GET /api/commanders/{id}/gear", server.handlePlayerGear)
	api("PUT /api/commanders/{id}/gear/{item}", server.handlePlayerSetGear)
	api("PUT /api/commanders/{id}/equip-skins/{item}", server.handlePlayerSetEquipSkin)
	api("PUT /api/commanders/{id}/spweapons/{item}", server.handlePlayerSetSpWeapon)
	api("DELETE /api/commanders/{id}/spweapons/{item}", server.handlePlayerDeleteSpWeapon)
	api("POST /api/commanders/{id}/grant-cosmetics", server.handlePlayerGrantCosmetics)
	api("GET /api/catalog/more", server.handlePlayerCatalog)

	api("GET /api/commanders/{id}/wardrobe", server.handlePlayerWardrobe)
	api("PUT /api/commanders/{id}/skins/{item}", server.handlePlayerSetSkin)
	api("DELETE /api/commanders/{id}/skins/{item}", server.handlePlayerDeleteSkin)
	api("PUT /api/commanders/{id}/virtual-items/{item}", server.handlePlayerSetVirtualItem)

	api("GET /api/commanders/{id}/mails", server.handlePlayerMails)
	api("DELETE /api/commanders/{id}/mails/{item}", server.handlePlayerDeleteMail)
	api("DELETE /api/commanders/{id}/compensations/{item}", server.handlePlayerDeleteCompensation)
}

type playerSecretary struct {
	ShipID uint32 `json:"ship_id"`
	Name   string `json:"name"`
	Level  uint32 `json:"level"`
	Rarity uint32 `json:"rarity"`
	Icon   string `json:"icon,omitempty"`
}

type playerGuild struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Duty uint32 `json:"duty"`
}

type playerProfile struct {
	commanderRow
	Exp       uint32           `json:"exp"`
	LevelExp  uint32           `json:"level_exp"`
	MaxLevel  uint32           `json:"max_level"`
	Manifesto string           `json:"manifesto"`
	Secretary *playerSecretary `json:"secretary"`
	Guild     *playerGuild     `json:"guild"`
	Counts    struct {
		Ships       int64  `json:"ships"`
		ShipBagMax  uint32 `json:"ship_bag_max"`
		Equipments  int64  `json:"equipments"`
		EquipBagMax uint32 `json:"equip_bag_max"`
		Skins       int64  `json:"skins"`
		Meowfficers int64  `json:"meowfficers"`
		Friends     int64  `json:"friends"`
		SpWeapons   int64  `json:"spweapons"`
		Furniture   int64  `json:"furniture"`
		Oaths       int64  `json:"oaths"`
	} `json:"counts"`
	SecondaryPassword bool `json:"secondary_password"`
	// the bags' capacities and the range each is set within
	Bags playerBags `json:"bags"`
}

type playerBags struct {
	Ship           uint32    `json:"ship_bag_max"`
	Equip          uint32    `json:"equip_bag_max"`
	Commander      uint32    `json:"commander_bag_max"`
	ShipLimits     [2]uint32 `json:"ship_limits"`
	EquipLimits    [2]uint32 `json:"equip_limits"`
	CommanderLimit [2]uint32 `json:"commander_limits"`
}

// handlePlayerProfile is a commander's resume, what it owns and its state.
func (server *Server) handlePlayerProfile(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	ctx, id := r.Context(), commander.ID
	maxLevel, levelExp := game.CommanderLevels()
	profile := playerProfile{
		commanderRow: toRow(id, commander.AccountID, commander.Name, commander.Level, commander.RegisteredAt, commander.LastOnline),
		Exp:          commander.Exp, MaxLevel: maxLevel, Manifesto: commander.Manifesto,
	}
	if commander.Level < maxLevel {
		profile.LevelExp = levelExp[commander.Level]
	}
	for _, online := range server.Game.Online() {
		if online.ID == id {
			profile.Online, profile.IP = true, online.IP
		}
	}
	counts, err := server.Store.AdminProfileCounts(ctx, id)
	if err != nil {
		return nil, err
	}
	c := &profile.Counts
	c.Ships, c.ShipBagMax, c.Equipments, c.EquipBagMax = counts.Ships, commander.ShipBagMax, counts.Equipments, commander.EquipBagMax
	c.Skins, c.Meowfficers, c.Friends, c.SpWeapons, c.Furniture, c.Oaths = counts.Skins, counts.Meowfficers, counts.Friends, counts.Spweapons, counts.Furniture, counts.Oaths
	profile.SecondaryPassword = counts.SecondaryPassword > 0
	b := &profile.Bags
	b.Ship, b.Equip, b.Commander = commander.ShipBagMax, commander.EquipBagMax, commander.CommanderBagMax
	b.ShipLimits, b.EquipLimits, b.CommanderLimit = game.BagLimits()
	secretaries, err := server.Store.ListSecretaries(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(secretaries) > 0 {
		ship, err := server.Store.GetShip(ctx, db.GetShipParams{ID: secretaries[0].ShipID, CommanderID: id})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			template := server.names[dropShip][ship.TemplateID]
			name := ship.Name
			if name == "" {
				name = template.Name
			}
			profile.Secretary = &playerSecretary{ShipID: ship.ID, Name: name, Level: ship.Level, Rarity: template.Rarity, Icon: server.names.shipIcon(ship.TemplateID, ship.SkinID)}
		}
	}
	guild, err := server.Store.AdminCommanderGuild(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		profile.Guild = &playerGuild{ID: guild.ID, Name: guild.Name, Duty: guild.Duty}
	}
	return profile, nil
}

// handlePlayerRename renames a commander.
func (server *Server) handlePlayerRename(r *http.Request) (any, error) {
	type body struct {
		Name string `json:"name"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.RenameCommander(ctx, id, b.Name)
	})
}

// handlePlayerLevel sets a commander's level, its exp back to 0.
func (server *Server) handlePlayerLevel(r *http.Request) (any, error) {
	type body struct {
		Level uint32 `json:"level"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetCommanderLevel(ctx, id, b.Level)
	})
}

// handlePlayerResetPassword removes a commander's secondary password.
func (server *Server) handlePlayerResetPassword(r *http.Request) (any, error) {
	return commanderEdit(server, r, func(ctx context.Context, id uint32, _ struct{}) (bool, error) {
		return server.Game.ResetSecondaryPassword(ctx, id)
	})
}

// handlePlayerExport is every row of a commander, by table.
func (server *Server) handlePlayerExport(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	return server.Game.ExportCommander(r.Context(), commander.ID)
}

// handlePlayerDelete deletes a commander and everything it has.
func (server *Server) handlePlayerDelete(r *http.Request) (any, error) {
	return commanderEdit(server, r, func(ctx context.Context, id uint32, _ struct{}) (bool, error) {
		return server.Game.DeleteCommander(ctx, id)
	})
}
