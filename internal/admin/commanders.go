package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/game"
	"github.com/ggmolly/belfast/internal/live"
	"github.com/ggmolly/belfast/internal/protobuf"
)

func itoa(id uint32) string { return strconv.FormatUint(uint64(id), 10) }

type commanderRow struct {
	ID           uint32 `json:"id"`
	AccountID    uint32 `json:"account_id"`
	Name         string `json:"name"`
	Level        uint32 `json:"level"`
	RegisteredAt int64  `json:"registered_at"`
	LastOnline   int64  `json:"last_online"`
	Online       bool   `json:"online"`
	IP           string `json:"ip,omitempty"`
}

// handleOnline lists the commanders playing.
func (server *Server) handleOnline(r *http.Request) (any, error) {
	commanders, ips, err := server.onlineCommanders(r.Context())
	if err != nil {
		return nil, err
	}
	rows := []commanderRow{}
	for _, commander := range commanders {
		row := toRow(commander.ID, commander.AccountID, commander.Name, commander.Level, commander.RegisteredAt, commander.LastOnline)
		row.Online, row.IP = true, ips[commander.ID]
		rows = append(rows, row)
	}
	return rows, nil
}

// onlineCommanders are the commanders playing, by id, and their IPs.
func (server *Server) onlineCommanders(ctx context.Context) ([]db.Commander, map[uint32]string, error) {
	online := server.Game.Online()
	ids := make([]uint32, 0, len(online))
	ips := make(map[uint32]string, len(online))
	for _, player := range online {
		ids = append(ids, player.ID)
		ips[player.ID] = player.IP
	}
	commanders, err := server.Store.ListCommandersByID(ctx, ids)
	return commanders, ips, err
}

func toRow(id, account uint32, name string, level uint32, registered, lastOnline int64) commanderRow {
	return commanderRow{ID: id, AccountID: account, Name: name, Level: level, RegisteredAt: registered, LastOnline: lastOnline}
}

// handleCommanders lists every commander.
func (server *Server) handleCommanders(r *http.Request) (any, error) {
	commanders, err := server.Store.ListCommanders(r.Context())
	if err != nil {
		return nil, err
	}
	rows := make([]commanderRow, 0, len(commanders))
	for _, c := range commanders {
		row := toRow(c.ID, c.AccountID, c.Name, c.Level, c.RegisteredAt, c.LastOnline)
		row.Online = server.Game.IsOnline(c.ID)
		rows = append(rows, row)
	}
	return rows, nil
}

type amount struct {
	ID     uint32 `json:"id"`
	Name   string `json:"name"`
	Rarity uint32 `json:"rarity,omitempty"`
	Icon   string `json:"icon,omitempty"`
	Count  uint32 `json:"count"`
}

type shipRow struct {
	ID         uint32 `json:"id"`
	TemplateID uint32 `json:"template_id"`
	Name       string `json:"name"`
	Rarity     uint32 `json:"rarity"`
	Level      uint32 `json:"level"`
	Intimacy   uint32 `json:"intimacy"`
	Locked     bool   `json:"locked"`
	Married    bool   `json:"married"`
}

type fleetRow struct {
	ID    uint32   `json:"id"`
	Name  string   `json:"name"`
	Ships []uint32 `json:"ships"`
}

type drop struct {
	Type  uint32 `json:"type"`
	ID    uint32 `json:"id"`
	Count uint32 `json:"count"`
	Name  string `json:"name,omitempty"`
}

type mailRow struct {
	ID          uint32 `json:"id"`
	Date        int64  `json:"date"`
	Title       string `json:"title"`
	Sender      string `json:"sender"`
	Content     string `json:"content"`
	Attachments []drop `json:"attachments"`
	Read        bool   `json:"read"`
	Claimed     bool   `json:"claimed"`
	Important   bool   `json:"important"`
	Archived    bool   `json:"archived"`
}

type commanderDetail struct {
	commanderRow
	Exp         uint32     `json:"exp"`
	ShipBagMax  uint32     `json:"ship_bag_max"`
	EquipBagMax uint32     `json:"equip_bag_max"`
	Manifesto   string     `json:"manifesto"`
	Resources   []amount   `json:"resources"`
	Items       []amount   `json:"items"`
	Equipments  []amount   `json:"equipments"`
	Ships       []shipRow  `json:"ships"`
	Fleets      []fleetRow `json:"fleets"`
	Mails       []mailRow  `json:"mails"`
}

// commander is the {id} commander, 404 when there is none.
func (server *Server) commander(r *http.Request) (db.Commander, error) {
	id, err := pathID(r)
	if err != nil {
		return db.Commander{}, err
	}
	commander, err := server.Store.GetCommander(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		return commander, notFound("no commander " + itoa(id))
	}
	return commander, err
}

// handleCommander is a commander's profile, bags, dock, fleets and mails.
func (server *Server) handleCommander(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	ctx, id := r.Context(), commander.ID
	detail := commanderDetail{
		commanderRow: toRow(id, commander.AccountID, commander.Name, commander.Level, commander.RegisteredAt, commander.LastOnline),
		Exp:          commander.Exp, ShipBagMax: commander.ShipBagMax, EquipBagMax: commander.EquipBagMax, Manifesto: commander.Manifesto,
		Resources: []amount{}, Items: []amount{}, Equipments: []amount{}, Ships: []shipRow{}, Fleets: []fleetRow{}, Mails: []mailRow{},
	}
	detail.Online = server.Game.IsOnline(id)
	resources, err := server.Store.ListResources(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, row := range resources {
		detail.Resources = append(detail.Resources, server.amount(dropResource, row.ResourceID, row.Amount))
	}
	items, err := server.Store.ListItems(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, row := range items {
		detail.Items = append(detail.Items, server.amount(dropItem, row.ItemID, row.Count))
	}
	equips, err := server.Store.ListEquipments(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, row := range equips {
		detail.Equipments = append(detail.Equipments, server.amount(dropEquip, row.EquipID, row.Count))
	}
	ships, err := server.Store.ListShipsByCommander(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, ship := range ships {
		template := server.names[dropShip][ship.TemplateID]
		name := ship.Name
		if name == "" {
			name = template.Name
		}
		detail.Ships = append(detail.Ships, shipRow{
			ID: ship.ID, TemplateID: ship.TemplateID, Name: name, Rarity: template.Rarity,
			Level: ship.Level, Intimacy: ship.Intimacy, Locked: ship.IsLocked, Married: ship.Propose != 0,
		})
	}
	fleets, err := server.Store.ListFleets(ctx, id)
	if err != nil {
		return nil, err
	}
	members, err := server.Store.ListFleetShips(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := len(fleets) - 1; i >= 0; i-- {
		fleet := fleetRow{ID: fleets[i].FleetID, Name: fleets[i].Name, Ships: []uint32{}}
		for _, member := range members {
			if member.FleetID == fleet.ID {
				fleet.Ships = append(fleet.Ships, member.ShipID)
			}
		}
		detail.Fleets = append(detail.Fleets, fleet)
	}
	inbox, err := server.Store.ListInboxMails(ctx, id)
	if err != nil {
		return nil, err
	}
	archived, err := server.Store.ListArchivedMailsPage(ctx, db.ListArchivedMailsPageParams{CommanderID: id, Count: 1000})
	if err != nil {
		return nil, err
	}
	for _, mail := range append(inbox, archived...) {
		row := mailRow{
			ID: mail.ID, Date: mail.Date, Title: mail.Title, Sender: mail.Sender, Content: mail.Content, Attachments: []drop{},
			Read: mail.IsRead, Claimed: mail.IsClaimed, Important: mail.IsImportant, Archived: mail.Archived,
		}
		for _, attachment := range mail.Attachments {
			row.Attachments = append(row.Attachments, drop{Type: attachment[0], ID: attachment[1], Count: attachment[2], Name: server.names.name(attachment[0], attachment[1])})
		}
		detail.Mails = append(detail.Mails, row)
	}
	sort.Slice(detail.Mails, func(i, j int) bool { return detail.Mails[i].ID > detail.Mails[j].ID })
	return detail, nil
}

func (server *Server) amount(kind, id, count uint32) amount {
	row := server.names[kind][id]
	return amount{ID: id, Name: row.Name, Rarity: row.Rarity, Icon: row.Icon, Count: count}
}

// handleKick disconnects a commander playing.
func (server *Server) handleKick(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	was, err := server.Game.Kick(commander.ID)
	if was {
		server.Game.Live.Publish(live.Event{Kind: live.Commander, ID: commander.ID})
	}
	return kicked(was, err)
}

// dropInfos checks the drops a request lists.
func dropInfos(drops []drop) ([]*protobuf.DROPINFO, error) {
	infos := make([]*protobuf.DROPINFO, 0, len(drops))
	for _, d := range drops {
		if d.Count == 0 {
			return nil, badRequest("a drop of 0")
		}
		infos = append(infos, &protobuf.DROPINFO{Type: proto.Uint32(d.Type), Id: proto.Uint32(d.ID), Number: proto.Uint32(d.Count)})
	}
	return infos, nil
}

// handleMail sends a mail, or a compensation when expire_hours is set.
func (server *Server) handleMail(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Title       string `json:"title"`
		Sender      string `json:"sender"`
		Content     string `json:"content"`
		Attachments []drop `json:"attachments"`
		ExpireHours uint32 `json:"expire_hours"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	attachments, err := dropInfos(body.Attachments)
	if err != nil {
		return nil, err
	}
	mail := game.Mail{Title: body.Title, Sender: body.Sender, Content: body.Content, Attachments: attachments}
	now := time.Now()
	if body.ExpireHours != 0 {
		id, err := game.SendCompensation(r.Context(), server.Store.Queries, commander.ID, mail, now, now.Add(time.Duration(body.ExpireHours)*time.Hour))
		if err != nil {
			return nil, badRequest(err.Error())
		}
		server.Game.Live.Publish(live.Event{Kind: live.Commander, ID: commander.ID})
		if err := server.Game.NotifyCompensations(r.Context(), commander.ID); err != nil {
			return nil, err
		}
		return map[string]any{"id": id, "kind": "compensation"}, nil
	}
	id, err := game.SendMail(r.Context(), server.Store.Queries, commander.ID, mail, now)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	server.Game.Live.Publish(live.Event{Kind: live.Commander, ID: commander.ID})
	if err := server.Game.NotifyMail(r.Context(), commander.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "kind": "mail"}, nil
}

// handleGrant stores drops for a commander, kicking it first if it plays.
func (server *Server) handleGrant(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	var body struct {
		Drops []drop `json:"drops"`
	}
	if err := readJSON(r, &body); err != nil {
		return nil, err
	}
	if len(body.Drops) == 0 {
		return nil, badRequest("nothing to grant")
	}
	drops, err := dropInfos(body.Drops)
	if err != nil {
		return nil, err
	}
	return kicked(server.Game.Grant(r.Context(), commander.ID, drops))
}

// handleCatalog searches the names of a drop type, for the grant form.
func (server *Server) handleCatalog(r *http.Request) (any, error) {
	kind, err := strconv.ParseUint(r.URL.Query().Get("type"), 10, 32)
	if err != nil || server.names[uint32(kind)] == nil {
		return nil, badRequest("type: want a drop type the back-office names")
	}
	return server.names.search(uint32(kind), r.URL.Query().Get("q"), 50), nil
}

// player_resource ids the commander screens' header shows: the client sums
// gems and free gems (Player:getTotalGem).
const (
	resourceGold    = 1
	resourceOil     = 2
	resourceGem     = 4
	resourceFreeGem = 14
)

type commanderSummary struct {
	commanderRow
	Gold   uint32 `json:"gold"`
	Oil    uint32 `json:"oil"`
	Gems   uint32 `json:"gems"`
	Counts struct {
		Items       int   `json:"items"`
		Ships       int64 `json:"ships"`
		Meowfficers int64 `json:"meowfficers"`
		Friends     int64 `json:"friends"`
		UnreadMail  int64 `json:"unread_mail"`
	} `json:"counts"`
}

// handleSummary is what every commander screen shows around its body: the
// header's resources and the sidebar's counts.
func (server *Server) handleSummary(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	ctx, id := r.Context(), commander.ID
	summary := commanderSummary{commanderRow: toRow(id, commander.AccountID, commander.Name, commander.Level, commander.RegisteredAt, commander.LastOnline)}
	summary.Online = server.Game.IsOnline(id)
	resources, err := server.Store.ListResources(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, row := range resources {
		switch row.ResourceID {
		case resourceGold:
			summary.Gold = row.Amount
		case resourceOil:
			summary.Oil = row.Amount
		case resourceGem, resourceFreeGem:
			summary.Gems += row.Amount
		}
	}
	items, err := server.Store.ListItems(ctx, id)
	if err != nil {
		return nil, err
	}
	summary.Counts.Items = len(items)
	if summary.Counts.Ships, err = server.Store.CountShipsByCommander(ctx, id); err != nil {
		return nil, err
	}
	if summary.Counts.Meowfficers, err = server.Store.CountMeowfficers(ctx, id); err != nil {
		return nil, err
	}
	if summary.Counts.Friends, err = server.Store.CountFriends(ctx, id); err != nil {
		return nil, err
	}
	if summary.Counts.UnreadMail, err = server.Store.CountUnreadMails(ctx, id); err != nil {
		return nil, err
	}
	return summary, nil
}
