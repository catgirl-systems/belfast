package game

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// The mailbox (controller/command/mail, model/proxy/mailproxy.lua,
// view/main/mail). No official capture has a mail: only SC_30001 {0, 0} at
// login and after battles, so the replies are built from the Lua alone.
//
// MailProxy keeps the inbox as positions 1..total_number in id order, oldest
// first: GetMailListCommand asks for the newest positions it hasn't seen
// ("new"), then older ones as the list scrolls ("next"), and removes deleted
// or archived mails from its count itself. Mails moved to the Archive leave
// the inbox and are listed apart (CS_30004).

// player_resource ids of the mail storage, next to ResourceGold / ResourceOil
const (
	ResourceExploit   = 3
	ResourceStoreGold = 16 // PlayerConst.ResStoreGold, "gold_mail"
	ResourceStoreOil  = 17 // PlayerConst.ResStoreOil, "oil_mail"
)

// CS_30002.type (GetMailListCommand)
const (
	mailListRange     = 1 // index_begin..index_end of the inbox
	mailListImportant = 2 // every important mail
	mailListRare      = 3 // every "Special" mail (BaseMail:IsRare)
)

// CS_30006.cmd, the 1-based position in MailProxy.DEAL_CMD_LIST
const (
	mailCmdRead = 1 + iota
	mailCmdImportant
	mailCmdUnimportant
	mailCmdDelete
	mailCmdAttachment
	mailCmdOverflow
	mailCmdMove
)

// MATCH_EXPRESSION.type (DealMailOperationCommand's filters); no expression
// at all is every inbox mail
const (
	mailMatchIDs       = 1 // these mail ids
	mailMatchResources = 2 // mails attaching only these resources / items
	mailMatchItems     = 3
)

// MAIL_INFO flags as Mail.Ctor reads them (model/vo/mail.lua)
const (
	mailAttachNone    = 0
	mailAttachUntaken = 1 // BaseMail.ATTACHMENT_UNTAKEN
	mailAttachTaken   = 2 // BaseMail.ATTACHMENT_TAKEN
	mailReadNo        = 1
	mailReadYes       = 2
)

// errArchiveFull is SC_30007 result 6, "mail_moveto_markroom_max".
var errArchiveFull error = refusal{"archive full", 6}

// user_level row, the Archive's capacity at a commander level
type mailLevelConfig struct {
	MaxMarkmail uint32 `json:"max_markmail"`
}

// mail_storeroom row: the storage's capacity at a level and what the next
// level costs (Player:GetExtendStoreCost)
type mailStoreroomConfig struct {
	GoldStore   uint32 `json:"gold_store"`
	OilStore    uint32 `json:"oil_store"`
	UpgradeGem  uint32 `json:"upgrade_gem"`
	UpgradeGold uint32 `json:"upgrade_gold"`
}

var (
	mailLevels     map[uint32]mailLevelConfig
	mailStorerooms map[uint32]mailStoreroomConfig
	// gameset max_gold / max_oil: what the storage can't take out past
	// (MailScene:UpdateStore)
	maxGold, maxOil uint32
	// the ids a mail can attach besides what storableDrop knows
	resourceIDs, furnitureIDs, virtualItemIDs map[uint32]struct{}
)

func loadMailData() error {
	var gameset map[string]uint32
	err := gamedata.LoadAll(
		gamedata.Load("user_level", &mailLevels),
		gamedata.Load("mail_storeroom", &mailStorerooms),
		gamedata.Load("player_resource", &resourceIDs),
		gamedata.Load("furniture_data_template", &furnitureIDs),
		gamedata.Load("item_virtual_data_statistics", &virtualItemIDs),
		func() (err error) {
			gameset, err = gamedata.Gameset()
			return err
		},
	)
	if err != nil {
		return err
	}
	if _, ok := mailStorerooms[1]; !ok {
		return gamedata.Missing("mail_storeroom", "level 1")
	}
	for key, value := range map[string]*uint32{"max_gold": &maxGold, "max_oil": &maxOil} {
		setting, ok := gameset[key]
		if !ok {
			return gamedata.Missing("gameset", "%s", key)
		}
		*value = setting
	}
	return nil
}

// Mail is what SendMail delivers. The sender shows as mail_sender_default
// ("Headquarters") when empty; BaseMail.Ctor replaces {ship_statistics:<id>}
// in it with that ship's name. Content may hold "\n" escapes.
type Mail struct {
	Title, Sender, Content string
	Attachments            []*protobuf.DROPINFO
}

// mailStorable is whether claiming a mail can store an attachment: what
// grantDrops keeps (resources, bag items, equipment, ships, furniture,
// virtual items, ship and gear skins, meowfficers, the island's warehouse
// items and outfits) and is in the game's tables. Frames, emojis... have no
// storage yet. The client adds island drops itself when it has its island
// loaded (Drop: IslandDropHelper.AddItems), else at the next login.
func mailStorable(kind, id uint32) bool {
	switch kind {
	case dropTypeResource:
		_, ok := resourceIDs[id]
		return ok
	case dropTypeFurniture:
		_, ok := furnitureIDs[id]
		return ok
	case dropTypeVItem:
		_, ok := virtualItemIDs[id]
		return ok
	case dropTypeIslandItem:
		_, ok := islandItems[id]
		return ok
	case dropTypeIslandDress:
		_, ok := islandDresses[id]
		return ok
	}
	return storableDrop(kind, id)
}

// check is the attachments of a mail as stored, refusing a mail without a
// title or with attachments belfast couldn't store when claimed.
func (mail Mail) check() (db.Drops, error) {
	if mail.Title == "" {
		return nil, fmt.Errorf("mail without a title")
	}
	var attachments db.Drops
	for _, drop := range mail.Attachments {
		kind, id, number := drop.GetType(), drop.GetId(), drop.GetNumber()
		if number == 0 || !mailStorable(kind, id) {
			return nil, fmt.Errorf("mail attachment %d:%d:%d can't be stored", kind, id, number)
		}
		attachments = append(attachments, [3]uint32{kind, id, number})
	}
	return attachments, nil
}

// SendMail delivers mail to a commander, arriving at now, and returns its id.
// Attachments belfast couldn't store when claimed are refused. The client
// only learns of it from the next SC_30001 (login, a battle's end, or
// Tx.SendMail's push).
func SendMail(ctx context.Context, queries *db.Queries, commanderID uint32, mail Mail, now time.Time) (uint32, error) {
	attachments, err := mail.check()
	if err != nil {
		return 0, err
	}
	return queries.CreateMail(ctx, db.CreateMailParams{
		CommanderID: commanderID,
		Date:        now.Unix(),
		Title:       mail.Title,
		Sender:      mail.Sender,
		Content:     mail.Content,
		Attachments: attachments,
	})
}

// SendMail delivers mail to the commander of the request, then tells the
// client its mailbox grew (SC_30001) once tx commits.
func (tx *Tx) SendMail(mail Mail) error {
	if _, err := SendMail(tx.ctx, tx.Queries, tx.commanderID, mail, time.Now()); err != nil {
		return err
	}
	if !tx.mailSent {
		tx.mailSent = true
		tx.afterEvents(func() error {
			counts, err := mailCounts(tx.ctx, tx.Queries, tx.commanderID)
			tx.push(counts)
			return err
		})
	}
	return nil
}

// mailCounts is SC_30001, MailProxy's inbox size and unread count, pushed at
// login and after battles (captured).
func mailCounts(ctx context.Context, queries *db.Queries, commanderID uint32) (*protobuf.SC_30001, error) {
	total, err := queries.CountInboxMails(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	unread, err := queries.CountUnreadMails(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	return &protobuf.SC_30001{UnreadNumber: proto.Uint32(uint32(unread)), TotalNumber: proto.Uint32(uint32(total))}, nil
}

// mailTitle is MAIL_INFO.title, "title||sender" as BaseMail.Ctor splits it.
func mailTitle(mail db.Mail) string {
	if mail.Sender == "" {
		return mail.Title
	}
	return mail.Title + "||" + mail.Sender
}

func mailAttachments(mail db.Mail) []*protobuf.DROPINFO {
	list := make([]*protobuf.DROPINFO, len(mail.Attachments))
	for i, drop := range mail.Attachments {
		list[i] = dropInfo(drop[0], drop[1], drop[2])
	}
	return list
}

// mailInfo is a MAIL_INFO as Mail.Ctor reads it.
func mailInfo(mail db.Mail) *protobuf.MAIL_INFO {
	info := &protobuf.MAIL_INFO{
		Id:             proto.Uint32(mail.ID),
		Date:           proto.Uint32(uint32(mail.Date)),
		Title:          proto.String(mailTitle(mail)),
		Content:        proto.String(mail.Content),
		AttachmentList: mailAttachments(mail),
		AttachFlag:     proto.Uint32(mailAttachNone),
		ReadFlag:       proto.Uint32(mailReadNo),
		ImpFlag:        proto.Uint32(0),
	}
	switch {
	case len(mail.Attachments) > 0 && mail.IsClaimed:
		info.AttachFlag = proto.Uint32(mailAttachTaken)
	case len(mail.Attachments) > 0:
		info.AttachFlag = proto.Uint32(mailAttachUntaken)
	}
	if mail.IsRead {
		info.ReadFlag = proto.Uint32(mailReadYes)
	}
	if mail.IsImportant {
		info.ImpFlag = proto.Uint32(1)
	}
	return info
}

// mailRare is BaseMail:IsRare, the "Special" tab: it attaches something
// besides gold, oil, merit and Wisdom Cubes.
func mailRare(mail db.Mail) bool {
	for _, drop := range mail.Attachments {
		common := (drop[0] == dropTypeResource && (drop[1] == ResourceGold || drop[1] == ResourceOil || drop[1] == ResourceExploit)) ||
			(drop[0] == dropTypeItem && drop[1] == ItemWisdomCube)
		if !common {
			return true
		}
	}
	return false
}

// mailUnclaimed is whether a mail still has attachments to take.
func mailUnclaimed(mail db.Mail) bool {
	return len(mail.Attachments) > 0 && !mail.IsClaimed
}

// mailClaimable is whether every attachment of a mail can be stored.
func mailClaimable(mail db.Mail) bool {
	for _, drop := range mail.Attachments {
		if !mailStorable(drop[0], drop[1]) {
			return false
		}
	}
	return true
}

// handleMailList answers GetMailListCommand and GetMailListToIndexCommand
// (CS_30002): inbox positions index_begin..index_end (1-based, both
// included, oldest first), or every important or "Special" mail. MailProxy
// lists them in the order sent.
func (game *Game) handleMailList(client *connection.Client, req *protobuf.CS_30002) error {
	ctx := context.Background()
	var mails []db.Mail
	var err error
	switch req.GetType() {
	case mailListRange:
		begin, end := max(req.GetIndexBegin(), 1), req.GetIndexEnd()
		if end >= begin {
			mails, err = game.DB.ListInboxMailsPage(ctx, db.ListInboxMailsPageParams{CommanderID: client.CommanderID, Skip: int64(begin - 1), Count: int64(end - begin + 1)})
		}
	case mailListImportant, mailListRare:
		mails, err = game.DB.ListInboxMails(ctx, client.CommanderID)
		mails = slices.DeleteFunc(mails, func(mail db.Mail) bool {
			if req.GetType() == mailListImportant {
				return !mail.IsImportant
			}
			return !mailRare(mail)
		})
	}
	if err != nil {
		return err
	}
	reply := &protobuf.SC_30003{MailList: make([]*protobuf.MAIL_INFO, len(mails))}
	for i, mail := range mails {
		reply.MailList[i] = mailInfo(mail)
	}
	return client.Send(reply)
}

// handleArchiveList answers GetCollectionMailListCommand (CS_30004): archive
// positions index_begin..index_end, oldest first. The client asks for
// SINGLE_MAIL_REQUIRE_SIZE more until it gets fewer.
func (game *Game) handleArchiveList(client *connection.Client, req *protobuf.CS_30004) error {
	ctx := context.Background()
	reply := &protobuf.SC_30005{}
	begin, end := max(req.GetIndexBegin(), 1), req.GetIndexEnd()
	if end < begin {
		return client.Send(reply)
	}
	mails, err := game.DB.ListArchivedMailsPage(ctx, db.ListArchivedMailsPageParams{CommanderID: client.CommanderID, Skip: int64(begin - 1), Count: int64(end - begin + 1)})
	if err != nil {
		return err
	}
	for _, mail := range mails {
		reply.MailList = append(reply.MailList, &protobuf.MAIL_SIMPLE_INFO{
			Id:             proto.Uint32(mail.ID),
			Date:           proto.Uint32(uint32(mail.Date)),
			Title:          proto.String(mailTitle(mail)),
			Content:        proto.String(mail.Content),
			AttachmentList: mailAttachments(mail),
		})
	}
	return client.Send(reply)
}

// mailMatch is CS_30006.match_list: the inbox mails it picks, in id order.
// No expression is every mail ("all"); ids picks those mails; resource and
// item ids ("drops", MailMgrWindow's "Mail Containing Only These Items") pick
// the mails with attachments that are all among them.
type mailMatch struct {
	ids     map[uint32]bool // nil: any
	drops   map[[2]uint32]bool
	byDrops bool
}

func newMailMatch(list []*protobuf.MATCH_EXPRESSION) mailMatch {
	var match mailMatch
	for _, expr := range list {
		switch expr.GetType() {
		case mailMatchIDs:
			if match.ids == nil {
				match.ids = map[uint32]bool{}
			}
			for _, id := range expr.GetArgList() {
				match.ids[id] = true
			}
		case mailMatchResources, mailMatchItems:
			kind := uint32(dropTypeResource)
			if expr.GetType() == mailMatchItems {
				kind = dropTypeItem
			}
			if match.drops == nil {
				match.drops = map[[2]uint32]bool{}
			}
			match.byDrops = true
			for _, id := range expr.GetArgList() {
				match.drops[[2]uint32{kind, id}] = true
			}
		}
	}
	return match
}

func (match mailMatch) picks(mail db.Mail) bool {
	if match.ids != nil && !match.ids[mail.ID] {
		return false
	}
	if !match.byDrops {
		return true
	}
	for _, drop := range mail.Attachments {
		if !match.drops[[2]uint32{drop[0], drop[1]}] {
			return false
		}
	}
	return len(mail.Attachments) > 0
}

// handleMailOperation answers DealMailOperationCommand (CS_30006): cmd on the
// inbox mails match_list picks. mail_id_list is the mails it changed, which
// MailProxy:DealMailOperation updates one by one, unread_number the inbox's
// unread count after it, drop_list what the client adds
// (PlayerConst.addTranDrop):
//   - read, important, unimportant: the mails whose flag changes
//   - delete: by id, any that isn't important (MailScene refuses those
//     itself); otherwise the read ones that aren't important and have nothing
//     left to claim ("Delete Read", mail_manage_tip_1)
//   - attachment: takes what the mails attach (claimMails)
//   - overflow: what attachment would take, changing nothing: the client asks
//     it first for the batch claims, checks drop_list against its caps and
//     confirms with mail_id_list (MailConfirmationWindow)
//   - move: to the Archive, taking their attachments; result 6 when the
//     Archive (user_level max_markmail at the commander's level) can't hold
//     them
func (game *Game) handleMailOperation(client *connection.Client, req *protobuf.CS_30006) error {
	ctx := context.Background()
	match := newMailMatch(req.GetMatchList())
	reply := &protobuf.SC_30007{Result: result(nil)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		commander, err := tx.GetCommander(ctx, client.CommanderID)
		if err != nil {
			return err
		}
		inbox, err := tx.ListInboxMails(ctx, client.CommanderID)
		if err != nil {
			return err
		}
		var picked []db.Mail
		for _, mail := range inbox {
			if match.picks(mail) {
				picked = append(picked, mail)
			}
		}
		var changed []db.Mail
		switch req.GetCmd() {
		case mailCmdRead:
			for _, mail := range picked {
				if !mail.IsRead {
					mail.IsRead = true
					changed = append(changed, mail)
				}
			}
		case mailCmdImportant, mailCmdUnimportant:
			important := req.GetCmd() == mailCmdImportant
			for _, mail := range picked {
				if mail.IsImportant != important {
					mail.IsImportant = important
					changed = append(changed, mail)
				}
			}
		case mailCmdDelete:
			for _, mail := range picked {
				if mail.IsImportant || (match.ids == nil && (!mail.IsRead || mailUnclaimed(mail))) {
					continue
				}
				if err := tx.DeleteMail(ctx, db.DeleteMailParams{ID: mail.ID, CommanderID: client.CommanderID}); err != nil {
					return err
				}
				reply.MailIdList = append(reply.MailIdList, mail.ID)
			}
		case mailCmdAttachment, mailCmdOverflow:
			claims := slices.DeleteFunc(picked, func(mail db.Mail) bool { return !mailUnclaimed(mail) || !mailClaimable(mail) })
			if len(claims) == 0 && match.ids != nil && slices.ContainsFunc(inbox, func(mail db.Mail) bool { return match.picks(mail) && mailUnclaimed(mail) }) {
				// asked for by id, but belfast can't store it
				return errRefused
			}
			if req.GetCmd() == mailCmdOverflow {
				var drops []*protobuf.DROPINFO
				for _, mail := range claims {
					drops = append(drops, mailAttachments(mail)...)
					reply.MailIdList = append(reply.MailIdList, mail.ID)
				}
				reply.DropList = mergeDrops(drops)
				break
			}
			if reply.DropList, err = tx.claimMails(commander, claims); err != nil {
				return err
			}
			for _, mail := range claims {
				mail.IsClaimed, mail.IsRead = true, true
				changed = append(changed, mail)
			}
		case mailCmdMove:
			archived, err := tx.CountArchivedMails(ctx, client.CommanderID)
			if err != nil {
				return err
			}
			if uint32(archived)+uint32(len(picked)) > mailLevels[commander.Level].MaxMarkmail {
				return errArchiveFull
			}
			var claims []db.Mail
			for _, mail := range picked {
				if mailUnclaimed(mail) {
					if !mailClaimable(mail) {
						return errRefused
					}
					claims = append(claims, mail)
				}
			}
			if reply.DropList, err = tx.claimMails(commander, claims); err != nil {
				return err
			}
			for _, mail := range picked {
				mail.Archived = true
				mail.IsClaimed = mail.IsClaimed || len(mail.Attachments) > 0
				mail.IsRead = true
				changed = append(changed, mail)
			}
		default:
			return errRefused
		}
		for _, mail := range changed {
			err := tx.SetMailFlags(ctx, db.SetMailFlagsParams{
				IsRead:      mail.IsRead,
				IsClaimed:   mail.IsClaimed,
				IsImportant: mail.IsImportant,
				Archived:    mail.Archived,
				ID:          mail.ID,
				CommanderID: client.CommanderID,
			})
			if err != nil {
				return err
			}
			reply.MailIdList = append(reply.MailIdList, mail.ID)
		}
		unread, err := tx.CountUnreadMails(ctx, client.CommanderID)
		reply.UnreadNumber = proto.Uint32(uint32(unread))
		return err
	})
	if err != nil && !refused(err) {
		return err
	}
	if err != nil {
		reply = &protobuf.SC_30007{Result: result(err)}
	}
	return client.Send(reply)
}

// claimMails takes the attachments of mails, refusing the lot when the dock
// or the gear depot can't hold them (bagsHold). Gold and oil go to the mail storage
// (mail_reward_tips), up to its mail_storeroom capacity, the rest is lost
// (mail_storeroom_max_1); the rest goes through grantDrops. It returns what
// the client adds: the storage's gold and oil as their own resources
// (ResStoreGold / ResStoreOil, drawn with the storage frame by
// Drop.UpdateDropCase), merged by kind.
func (tx *Tx) claimMails(commander db.Commander, mails []db.Mail) ([]*protobuf.DROPINFO, error) {
	var drops []*protobuf.DROPINFO
	for _, mail := range mails {
		drops = append(drops, mailAttachments(mail)...)
	}
	drops = mergeDrops(drops)
	if len(drops) == 0 {
		return nil, nil
	}
	if err := tx.bagsHold(commander, drops); err != nil {
		return nil, err
	}
	stored, err := tx.storeResources()
	if err != nil {
		return nil, err
	}
	storeroom := mailStorerooms[commander.MailStoreroomLv]
	var granted, rest []*protobuf.DROPINFO
	for _, drop := range drops {
		store, capacity := uint32(0), uint32(0)
		switch {
		case drop.GetType() == dropTypeResource && drop.GetId() == ResourceGold:
			store, capacity = ResourceStoreGold, storeroom.GoldStore
		case drop.GetType() == dropTypeResource && drop.GetId() == ResourceOil:
			store, capacity = ResourceStoreOil, storeroom.OilStore
		default:
			rest = append(rest, drop)
			granted = append(granted, drop)
			continue
		}
		amount := min(drop.GetNumber(), capacity-min(stored[store], capacity))
		if amount == 0 {
			continue
		}
		if err := tx.AddResource(store, amount); err != nil {
			return nil, err
		}
		stored[store] += amount
		granted = append(granted, dropInfo(dropTypeResource, store, amount))
	}
	if err := tx.grantDrops(rest); err != nil {
		return nil, err
	}
	return mergeDrops(granted), nil
}

// bagsHold refuses drops the dock (ship_bag_max) or the gear depot
// (equip_bag_max, plus guild technologies' slots) can't hold
// (CheckOverflow's "ship" and "equip").
func (tx *Tx) bagsHold(commander db.Commander, drops []*protobuf.DROPINFO) error {
	var ships, equipment uint32
	for _, drop := range drops {
		switch drop.GetType() {
		case dropTypeShip:
			ships += drop.GetNumber()
		case dropTypeEquip:
			equipment += drop.GetNumber()
		}
	}
	if ships > 0 {
		// META ships take no dock space (dockHasRoom)
		if err := tx.dockHasRoom(ships); err != nil {
			return err
		}
	}
	if equipment > 0 {
		owned, err := tx.CountEquipments(tx.ctx, tx.commanderID)
		if err != nil {
			return err
		}
		bonus, err := tx.guildTechBonus(guildTechEquipBag)
		if err != nil {
			return err
		}
		if uint32(owned)+equipment > commander.EquipBagMax+bonus {
			return errRefused
		}
	}
	return nil
}

// storeResources is the gold and oil in the mail storage.
func (tx *Tx) storeResources() (map[uint32]uint32, error) {
	rows, err := tx.ListResources(tx.ctx, tx.commanderID)
	if err != nil {
		return nil, err
	}
	stored := map[uint32]uint32{}
	for _, row := range rows {
		if row.ResourceID == ResourceStoreGold || row.ResourceID == ResourceStoreOil {
			stored[row.ResourceID] = row.Amount
		}
	}
	return stored, nil
}

// mergeDrops adds up the drops of the same kind and id, in first-seen order
// (PlayerConst.MergeSameDrops).
func mergeDrops(drops []*protobuf.DROPINFO) []*protobuf.DROPINFO {
	var merged []*protobuf.DROPINFO
	index := map[[2]uint32]int{}
	for _, drop := range drops {
		key := [2]uint32{drop.GetType(), drop.GetId()}
		if i, ok := index[key]; ok {
			merged[i].Number = proto.Uint32(merged[i].GetNumber() + drop.GetNumber())
			continue
		}
		index[key] = len(merged)
		merged = append(merged, dropInfo(key[0], key[1], drop.GetNumber()))
	}
	return merged
}

// handleDeleteArchivedMail answers DeleteCollectionMailCommand (CS_30008):
// an Archive mail deleted for good.
func (game *Game) handleDeleteArchivedMail(client *connection.Client, req *protobuf.CS_30008) error {
	n, err := game.DB.DeleteArchivedMail(context.Background(), db.DeleteArchivedMailParams{ID: req.GetMailId(), CommanderID: client.CommanderID})
	if err != nil {
		return err
	}
	if n == 0 {
		return client.Send(&protobuf.SC_30009{Result: result(errRefused)})
	}
	return client.Send(&protobuf.SC_30009{Result: result(nil)})
}

// handleExtendMailStorage answers ExtendStoreCapacityCommand (CS_30010): the
// mail storage's next level, paid in arg, the gems (ResDiamond) or the gold
// of the current mail_storeroom row's upgrade_gem / upgrade_gold. On result 0
// the client takes the price and raises mailStoreLevel itself. EN's
// mail_storeroom has one level, so the client hides the button.
func (game *Game) handleExtendMailStorage(client *connection.Client, req *protobuf.CS_30010) error {
	ctx := context.Background()
	err := game.tx(ctx, client, func(tx *Tx) error {
		commander, err := tx.GetCommander(ctx, client.CommanderID)
		if err != nil {
			return err
		}
		current := mailStorerooms[commander.MailStoreroomLv]
		if _, ok := mailStorerooms[commander.MailStoreroomLv+1]; !ok {
			return errRefused
		}
		switch {
		case req.GetArg() == ResourceGem && current.UpgradeGem > 0:
			err = tx.SpendShopResource(ResourceGem, current.UpgradeGem)
		case req.GetArg() == ResourceGold && current.UpgradeGold > 0:
			err = tx.SpendResource(ResourceGold, current.UpgradeGold)
		default:
			err = errRefused
		}
		if err != nil {
			return err
		}
		return tx.SetMailStoreroomLv(ctx, db.SetMailStoreroomLvParams{MailStoreroomLv: commander.MailStoreroomLv + 1, ID: commander.ID})
	})
	if err != nil && !refused(err) {
		return err
	}
	return client.Send(&protobuf.SC_30011{Result: result(err)})
}

// handleMailStorageWithdraw answers GetStoreResCommand (CS_30012): oil and
// gold out of the mail storage, which the client moves itself on result 0.
// MailScene:UpdateStore caps each at what keeps the commander under gameset
// max_gold / max_oil; more than that, or than is stored, is refused.
func (game *Game) handleMailStorageWithdraw(client *connection.Client, req *protobuf.CS_30012) error {
	ctx := context.Background()
	err := game.tx(ctx, client, func(tx *Tx) error {
		rows, err := tx.ListResources(ctx, client.CommanderID)
		if err != nil {
			return err
		}
		owned := map[uint32]uint32{}
		for _, row := range rows {
			owned[row.ResourceID] = row.Amount
		}
		for _, take := range []struct{ resource, store, amount, cap uint32 }{
			{ResourceOil, ResourceStoreOil, req.GetOil(), maxOil},
			{ResourceGold, ResourceStoreGold, req.GetGold(), maxGold},
		} {
			if take.amount == 0 {
				continue
			}
			if owned[take.resource]+take.amount > take.cap {
				return errRefused
			}
			if err := tx.SpendResource(take.store, take.amount); err != nil {
				return err
			}
			if err := tx.AddResource(take.resource, take.amount); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !refused(err) {
		return err
	}
	return client.Send(&protobuf.SC_30013{Result: result(err)})
}

// handleMailTitles answers GetMailTitleCommand (CS_30014): the titles of the
// mails a batch claim would take (MailConfirmationWindow lists them).
func (game *Game) handleMailTitles(client *connection.Client, req *protobuf.CS_30014) error {
	reply := &protobuf.SC_30015{}
	if len(req.GetIdList()) == 0 {
		return client.Send(reply)
	}
	mails, err := game.DB.ListMailsByID(context.Background(), db.ListMailsByIDParams{CommanderID: client.CommanderID, Ids: req.GetIdList()})
	if err != nil {
		return err
	}
	for _, mail := range mails {
		reply.MailTitleList = append(reply.MailTitleList, &protobuf.MAIL_TITLE{Id: proto.Uint32(mail.ID), Title: proto.String(mail.Title)})
	}
	return client.Send(reply)
}

// handleLoveLetterMailCheck answers CheckLoveLetterItemMailCommand
// (CS_30016, ItemInfoLayer's "Scan" on a Valentine Gift: an item listed in
// gameset loveletter_item_old_year or loveletter_2018_2021, for the ship
// group groupid): the years of that ship's Valentine mail that are neither in
// the inbox nor the Archive and can be resent (CS_30018). Belfast never sent
// a Valentine mail and doesn't know their text (it is server-side), so
// there is never one to resend: the client shows loveletter_recover_tip7
// and keeps "Resend" greyed. Built from the Lua alone.
func (game *Game) handleLoveLetterMailCheck(client *connection.Client, req *protobuf.CS_30016) error {
	return client.Send(&protobuf.SC_30017{})
}

// handleLoveLetterMailRepair answers RepairLoveLetterItemMailCommand
// (CS_30018): resend the Valentine mail of a year to the Archive, using the
// item. ret 0 (the client takes the item and adds drop_list), 6 Archive
// full (loveletter_recover_tip5), 7 the mail is already there
// (loveletter_recover_tip3), 40 bag full. The client only sends it for a
// year CS_30016 listed, which belfast never does (see above): ret 7.
func (game *Game) handleLoveLetterMailRepair(client *connection.Client, req *protobuf.CS_30018) error {
	return client.Send(&protobuf.SC_30019{Ret: proto.Uint32(7)})
}
