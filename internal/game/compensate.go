package game

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// Compensations (model/proxy/compensateproxy.lua, view/main/compensate):
// mails with an expiry, kept apart from the mailbox. SC_30101 at login
// tells CompensateProxy how many are still to claim and when the last one
// expires; the main screen shows its button while some are left
// (MainActCompensatBtn, hasRewardCount) and CompensateScene asks for the
// list (CS_30102) on entering, showing the ones not expired yet. The
// official captures only have SC_30101 {0, 0}: the rest is built from the
// Lua alone.

// SendCompensation delivers mail to a commander as a compensation sent at now
// and gone at expires, and returns its id. Attachments are refused as for
// SendMail. The client only learns of it from the next SC_30101: its next
// login, or Game.NotifyCompensations.
func SendCompensation(ctx context.Context, queries *db.Queries, commanderID uint32, mail Mail, now, expires time.Time) (uint32, error) {
	attachments, err := mail.check()
	if err != nil {
		return 0, err
	}
	if !expires.After(now) {
		return 0, fmt.Errorf("compensation expiring before it is sent")
	}
	return queries.CreateCompensation(ctx, db.CreateCompensationParams{
		CommanderID: commanderID,
		Date:        now.Unix(),
		Expires:     expires.Unix(),
		Title:       mail.Title,
		Sender:      mail.Sender,
		Content:     mail.Content,
		Attachments: attachments,
	})
}

// compensationCounts is what SC_30101 and SC_30105 tell CompensateProxy of
// the compensations not expired at now: how many are left to claim (number)
// and when the last of them expires (max_timestamp, 0 for none).
func compensationCounts(list []db.Compensation) (number, latest uint32) {
	for _, compensation := range list {
		if !compensation.IsClaimed {
			number++
		}
		latest = max(latest, uint32(compensation.Expires))
	}
	return number, latest
}

// compensationState is SC_30101, pushed at login and by
// NotifyCompensations.
func compensationState(ctx context.Context, queries *db.Queries, commanderID uint32, now time.Time) (*protobuf.SC_30101, error) {
	list, err := queries.ListCompensations(ctx, db.ListCompensationsParams{CommanderID: commanderID, Now: now.Unix()})
	if err != nil {
		return nil, err
	}
	number, latest := compensationCounts(list)
	return &protobuf.SC_30101{Number: proto.Uint32(number), MaxTimestamp: proto.Uint32(latest)}, nil
}

// handleCompensations answers GetCompensateCommand (CS_30102, type always 0):
// the compensations not expired yet, as CompensateData.New reads them
// (title "title||sender", attach_flag nonzero once claimed).
func (game *Game) handleCompensations(client *connection.Client, req *protobuf.CS_30102) error {
	list, err := game.DB.ListCompensations(context.Background(), db.ListCompensationsParams{CommanderID: client.CommanderID, Now: time.Now().Unix()})
	if err != nil {
		return err
	}
	reply := &protobuf.SC_30103{TimeRewardList: make([]*protobuf.TIME_REWARD_INFO, len(list))}
	for i, compensation := range list {
		mail := db.Mail{Title: compensation.Title, Sender: compensation.Sender, Attachments: compensation.Attachments}
		flag := uint32(0)
		if compensation.IsClaimed {
			flag = 1
		}
		reply.TimeRewardList[i] = &protobuf.TIME_REWARD_INFO{
			Id:             proto.Uint32(compensation.ID),
			Timestamp:      proto.Uint32(uint32(compensation.Expires)),
			Title:          proto.String(mailTitle(mail)),
			Text:           proto.String(compensation.Content),
			AttachmentList: mailAttachments(mail),
			AttachFlag:     proto.Uint32(flag),
			SendTime:       proto.Uint32(uint32(compensation.Date)),
		}
	}
	return client.Send(reply)
}

// handleClaimCompensation answers GetCompensateRewardCommand (CS_30104): the
// attachments of a compensation, which the client adds from drop_list
// (PlayerConst.addTranDrop) before marking it claimed and taking number and
// max_timestamp as for SC_30101. The client first checks the gold and oil
// caps and the dock and depot (CheckOverflow), which belfast checks again:
// gold and oil go straight to the commander, up to gameset max_gold /
// max_oil. A compensation claimed, expired or not the commander's is
// refused; result 6 (mail_moveto_markroom_max) is never sent.
func (game *Game) handleClaimCompensation(client *connection.Client, req *protobuf.CS_30104) error {
	ctx := context.Background()
	now := time.Now()
	reply := &protobuf.SC_30105{Result: result(nil)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		compensation, err := tx.GetCompensation(ctx, db.GetCompensationParams{ID: req.GetRewardId(), CommanderID: client.CommanderID})
		if errors.Is(err, sql.ErrNoRows) {
			return errRefused
		}
		if err != nil {
			return err
		}
		if compensation.IsClaimed || compensation.Expires <= now.Unix() {
			return errRefused
		}
		commander, err := tx.GetCommander(ctx, client.CommanderID)
		if err != nil {
			return err
		}
		drops := mergeDrops(mailAttachments(db.Mail{Attachments: compensation.Attachments}))
		if err := tx.bagsHold(commander, drops); err != nil {
			return err
		}
		if err := tx.underResourceCaps(drops); err != nil {
			return err
		}
		if err := tx.grantDrops(drops); err != nil {
			return err
		}
		if err := tx.SetCompensationClaimed(ctx, db.SetCompensationClaimedParams{ID: compensation.ID, CommanderID: client.CommanderID}); err != nil {
			return err
		}
		reply.DropList = drops
		list, err := tx.ListCompensations(ctx, db.ListCompensationsParams{CommanderID: client.CommanderID, Now: now.Unix()})
		if err != nil {
			return err
		}
		number, latest := compensationCounts(list)
		reply.Number, reply.MaxTimestamp = proto.Uint32(number), proto.Uint32(latest)
		return nil
	})
	if err != nil && !refused(err) {
		return err
	}
	if err != nil {
		reply = &protobuf.SC_30105{Result: result(err), Number: proto.Uint32(0), MaxTimestamp: proto.Uint32(0)}
	}
	return client.Send(reply)
}

// underResourceCaps refuses drops that would take the commander's gold or
// oil past gameset max_gold / max_oil (CheckOverflow's "gold" and "oil").
func (tx *Tx) underResourceCaps(drops []*protobuf.DROPINFO) error {
	var gold, oil uint32
	for _, drop := range drops {
		switch {
		case drop.GetType() == dropTypeResource && drop.GetId() == ResourceGold:
			gold += drop.GetNumber()
		case drop.GetType() == dropTypeResource && drop.GetId() == ResourceOil:
			oil += drop.GetNumber()
		}
	}
	if gold == 0 && oil == 0 {
		return nil
	}
	rows, err := tx.ListResources(tx.ctx, tx.commanderID)
	if err != nil {
		return err
	}
	owned := map[uint32]uint32{}
	for _, row := range rows {
		owned[row.ResourceID] = row.Amount
	}
	if (gold > 0 && owned[ResourceGold]+gold > maxGold) || (oil > 0 && owned[ResourceOil]+oil > maxOil) {
		return errRefused
	}
	return nil
}
