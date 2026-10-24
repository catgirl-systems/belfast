package game

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/logger"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// juusPosts are the Juus posts the client can't build from its tables
// (activity_ins_template is_active 1: InitByServer), as first pushed by the
// official server: an SC_11706 in text format, server/juus_posts.txtpb of the
// data dir. Without it there are no posts.
var juusPosts map[uint32]*protobuf.INS_MESSAGE

func loadJuusPosts() error {
	juusPosts = map[uint32]*protobuf.INS_MESSAGE{}
	text, err := gamedata.ServerFile("juus_posts.txtpb")
	if text == nil || err != nil {
		return err
	}
	var list protobuf.SC_11706
	if err := prototext.Unmarshal(text, &list); err != nil {
		return fmt.Errorf("juus posts: %w", err)
	}
	for _, post := range list.InsMessageList {
		juusPosts[post.GetId()] = post
	}
	return nil
}

// juusReply is an NPC answer to the commander's comment, posted delay
// seconds after it.
type juusReply struct {
	ID    uint32 `json:"id"`
	Delay uint32 `json:"delay"`
}

// juusOptionReplies are the NPC answers to a comment option (post id ->
// 1-based option -> answers), as the official server posted them:
// activity_ins_npc_template has the answers' ids but not which option they
// answer nor when. They are server content, server/juus_option_replies.json
// of the data dir ({"<post id>": {"<option>": [{"id": .., "delay": ..}]}});
// options it doesn't list, or all of them without the file, get no answer.
var juusOptionReplies map[uint32]map[uint32][]juusReply

func loadJuusOptionReplies() error {
	juusOptionReplies = nil
	text, err := gamedata.ServerFile("juus_option_replies.json")
	if text == nil || err != nil {
		return err
	}
	if err := json.Unmarshal(text, &juusOptionReplies); err != nil {
		return fmt.Errorf("juus option replies: %w", err)
	}
	return nil
}

// CS_11701 cmd values (ActivityConst.INSTAGRAM_OP_*)
const (
	juusOpUpdate   = 1 // also sent to activate a post once its time came
	juusOpLike     = 3
	juusOpMarkRead = 5
)

// activity_ins_chat_group row: a chat topic and the character it is with
type juusTopicConfig struct {
	ShipGroup uint32 `json:"ship_group"`
}

// activity_ins_chat_language row: a chat line; type 4 is a red packet whose
// param is an activity_ins_redpackage id (a number or a string)
type juusWordConfig struct {
	Type  uint32          `json:"type"`
	Param json.RawMessage `json:"param"`
}

const juusWordRedPacket = 4

// activity_ins_redpackage row: content is the drop
type juusRedPacketConfig struct {
	Content []uint32 `json:"content"`
}

var (
	juusTopicConfigs map[uint32]juusTopicConfig
	// juusRedPackets are the drops of the red packet chat lines, by
	// activity_ins_chat_language id
	juusRedPackets map[uint32][]uint32
)

// loadJuusData reads the Juus chats and the posts the server sends.
func loadJuusData() error {
	var words map[uint32]juusWordConfig
	var packets map[uint32]juusRedPacketConfig
	err := gamedata.LoadAll(
		gamedata.Load("activity_ins_chat_group", &juusTopicConfigs),
		gamedata.Load("activity_ins_chat_language", &words),
		gamedata.Load("activity_ins_redpackage", &packets),
		gamedata.Load("activity_ins_ship_group_template", &juusChatConfigs),
		loadJuusPosts,
		loadJuusOptionReplies,
	)
	if err != nil {
		return err
	}
	juusRedPackets = map[uint32][]uint32{}
	for chatID, word := range words {
		if word.Type != juusWordRedPacket {
			continue
		}
		var id uint64
		var text string
		var number float64
		if json.Unmarshal(word.Param, &text) == nil {
			id, _ = strconv.ParseUint(text, 10, 32)
		} else if json.Unmarshal(word.Param, &number) == nil {
			id = uint64(number)
		}
		if packet, ok := packets[uint32(id)]; ok && len(packet.Content) >= 3 {
			juusRedPackets[chatID] = packet.Content
		}
	}
	return nil
}

// juusMessage is a post as the commander sees it: liked, read, and their
// comment with the answers it got.
func juusMessage(row db.CommanderJuusPost) *protobuf.INS_MESSAGE {
	base, ok := juusPosts[row.PostID]
	if !ok {
		return nil
	}
	post := proto.Clone(base).(*protobuf.INS_MESSAGE)
	if row.IsGood {
		post.IsGood = proto.Uint32(1)
		post.Good = proto.Uint32(post.GetGood() + 1)
	}
	if row.IsRead {
		post.IsRead = proto.Uint32(1)
	}
	if row.CommentDiscuss != 0 {
		for _, discuss := range post.PlayerDiscuss {
			index := int(row.CommentIndex) - 1
			if discuss.GetId() != row.CommentDiscuss || index < 0 || index >= len(discuss.TextList) {
				continue
			}
			discuss.Text = proto.String(discuss.TextList[index])
			discuss.TextList = nil
			discuss.Time = proto.Uint32(row.CommentTime)
			for _, reply := range juusOptionReplies[row.PostID][row.CommentIndex] {
				discuss.NpcReply = append(discuss.NpcReply, reply.ID)
				post.NpcReply = append(post.NpcReply, &protobuf.INS_NPC{
					Id:   proto.Uint32(reply.ID),
					Time: proto.Uint32(row.CommentTime + reply.Delay),
					Text: proto.String(""), // the capture sends none
				})
			}
		}
	}
	return post
}

// handleJuusPosts answers the client asking for posts by id
// (RequestNew/OldInstagramDataCommand) with the ones pushed to the commander;
// a new commander has none (every SC_11706 of the capture is empty).
func (game *Game) handleJuusPosts(client *connection.Client, req *protobuf.CS_11705) error {
	rows, err := game.DB.ListJuusPosts(context.Background(), client.CommanderID)
	if err != nil {
		return err
	}
	byID := make(map[uint32]db.CommanderJuusPost, len(rows))
	for _, row := range rows {
		byID[row.PostID] = row
	}
	reply := &protobuf.SC_11706{}
	for _, id := range req.GetIdList() {
		if row, ok := byID[id]; ok {
			if post := juusMessage(row); post != nil {
				reply.InsMessageList = append(reply.InsMessageList, post)
			}
		}
	}
	return client.Send(reply)
}

// handleJuusPostOp answers InstagramActivityCommand on a post: cmd 1
// activates it (the client's timer sends it once the post's time came) or
// refreshes it, 3 likes, 4 shares, 5 marks it read. The reply is the post.
func (game *Game) handleJuusPostOp(client *connection.Client, req *protobuf.CS_11701) error {
	ctx := context.Background()
	if req.GetCmd() == juusOpUpdate {
		if juusPosts[req.GetId()] == nil {
			logger.WithFields("Juus", logger.FieldValue("id", req.GetId()), logger.FieldValue("cmd", req.GetCmd())).Warn("post op refused: not a known post")
			return client.Send(&protobuf.SC_11702{Result: result(errRefused)})
		}
		if err := game.DB.CreateJuusPost(ctx, db.CreateJuusPostParams{CommanderID: client.CommanderID, PostID: req.GetId()}); err != nil {
			return err
		}
	}
	var row db.CommanderJuusPost
	var err error
	switch req.GetCmd() {
	case juusOpLike:
		row, err = game.DB.LikeJuusPost(ctx, db.LikeJuusPostParams{CommanderID: client.CommanderID, PostID: req.GetId()})
	case juusOpMarkRead:
		row, err = game.DB.ReadJuusPost(ctx, db.ReadJuusPostParams{CommanderID: client.CommanderID, PostID: req.GetId()})
	default:
		row, err = game.DB.GetJuusPost(ctx, db.GetJuusPostParams{CommanderID: client.CommanderID, PostID: req.GetId()})
	}
	if errors.Is(err, sql.ErrNoRows) {
		// not a post the commander has
		logger.WithFields("Juus", logger.FieldValue("id", req.GetId()), logger.FieldValue("cmd", req.GetCmd())).Warn("post op refused: commander doesn't have the post")
		return client.Send(&protobuf.SC_11702{Result: result(errRefused)})
	} else if err != nil {
		return err
	}
	return client.Send(&protobuf.SC_11702{Result: result(nil), Data: juusMessage(row)})
}

// handleJuusComment posts the comment option the commander picked (discuss is
// the post's player_discuss id, index the 1-based option), the reply is the
// post with it and the NPC answers.
func (game *Game) handleJuusComment(client *connection.Client, req *protobuf.CS_11703) error {
	ctx := context.Background()
	key := db.GetJuusPostParams{CommanderID: client.CommanderID, PostID: req.GetId()}
	row, err := game.DB.GetJuusPost(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return client.Send(&protobuf.SC_11704{Result: result(errRefused)})
	} else if err != nil {
		return err
	}
	valid := false
	if post := juusMessage(row); post != nil && row.CommentDiscuss == 0 {
		for _, discuss := range post.PlayerDiscuss {
			if discuss.GetId() == req.GetDiscuss() && req.GetIndex() >= 1 && int(req.GetIndex()) <= len(discuss.TextList) {
				valid = true
			}
		}
	}
	if !valid {
		return client.Send(&protobuf.SC_11704{Result: result(errRefused)})
	}
	if err := game.DB.CommentJuusPost(ctx, db.CommentJuusPostParams{
		CommentDiscuss: req.GetDiscuss(),
		CommentIndex:   req.GetIndex(),
		CommentTime:    uint32(time.Now().Unix()),
		CommanderID:    client.CommanderID,
		PostID:         req.GetId(),
	}); err != nil {
		return err
	}
	if row, err = game.DB.GetJuusPost(ctx, key); err != nil {
		return err
	}
	return client.Send(&protobuf.SC_11704{Result: result(nil), Data: juusMessage(row)})
}

// juusGroups is SC_11711, the Juus chats (InstagramChatProxy): the unlocked
// topics grouped by character, each with the commander's answers, and the
// chat's settings: background skin, care, and the topic shown (the latest
// one unlocked until the commander picks one). CS_11710 replies with it.
func (game *Game) juusGroups(ctx context.Context, commanderID uint32) (*protobuf.SC_11711, error) {
	topics, err := game.DB.ListJuusTopics(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	rows, err := game.DB.ListJuusGroups(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	settings := make(map[uint32]db.CommanderJuusGroup, len(rows))
	for _, row := range rows {
		settings[row.GroupID] = row
	}
	replies, err := game.DB.ListJuusReplies(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	answers := map[uint32][]*protobuf.KEYVALUE_P11{}
	for _, reply := range replies {
		answers[reply.TopicID] = append(answers[reply.TopicID], &protobuf.KEYVALUE_P11{Key: proto.Uint32(reply.ChatID), Value: proto.Uint32(reply.Value)})
	}
	msg := &protobuf.SC_11711{}
	groups := map[uint32]*protobuf.JUUS_GROUP{}
	for _, topic := range topics {
		shipGroup := juusTopicConfigs[topic.TopicID].ShipGroup
		group, ok := groups[shipGroup]
		if !ok {
			group = &protobuf.JUUS_GROUP{Id: proto.Uint32(shipGroup)}
			groups[shipGroup] = group
			msg.Groups = append(msg.Groups, group)
		}
		group.CurChatGroup = proto.Uint32(topic.TopicID)
		readFlag := uint32(0)
		if topic.ReadFlag {
			readFlag = 1
		}
		chat := &protobuf.JUUS_CHAT_GROUP{Id: proto.Uint32(topic.TopicID), OpTime: proto.Uint32(topic.OpTime), ReadFlag: proto.Uint32(readFlag), ReplyList: answers[topic.TopicID]}
		group.ChatGroupList = append(group.ChatGroupList, chat)
	}
	for _, group := range msg.Groups {
		setting := settings[group.GetId()]
		group.SkinId, group.Favorite = proto.Uint32(setting.SkinID), proto.Uint32(setting.Favorite)
		if setting.CurTopic == 0 {
			continue
		}
		for _, chat := range group.ChatGroupList {
			if chat.GetId() == setting.CurTopic {
				group.CurChatGroup = proto.Uint32(setting.CurTopic)
			}
		}
	}
	return msg, nil
}

// activity_ins_ship_group_template row: a chat's character, type 1 a ship
// (its id is the ship group), 2 a group chat
type juusChatConfig struct {
	Type uint32 `json:"type"`
}

const juusChatShip = 1

var juusChatConfigs map[uint32]juusChatConfig

// juusChatGroup tells whether the commander has a chat with group: one of
// its topics unlocked.
func (game *Game) juusChatGroup(ctx context.Context, commanderID, group uint32) (bool, error) {
	topics, err := game.DB.ListJuusTopics(ctx, commanderID)
	if err != nil {
		return false, err
	}
	for _, topic := range topics {
		if juusTopicConfigs[topic.TopicID].ShipGroup == group {
			return true, nil
		}
	}
	return false, nil
}

// juusSkinAllowed is whether a ship's chat can take skinID as background,
// the skins InstagramChat:GetSkins offers unlocked: the ship's own or one
// its group shares (ship_data_group share_group_id), owned, or its default
// skin once the ship is in the archives, its oath skin once oathed, its
// retrofit skin once retrofitted (the lockFrame of the background picker,
// juuschat_background_tip2).
func (game *Game) juusSkinAllowed(ctx context.Context, commanderID, group, skinID uint32) (bool, error) {
	skin, ok := shipSkins[skinID]
	if !ok || (skin.group != group && !slices.Contains(shareGroups[group], skin.group)) {
		return false, nil
	}
	owned, err := ownedSkins(ctx, game.DB.Queries, commanderID, time.Now())
	if err != nil {
		return false, err
	}
	if hasSkin(owned, skinID) {
		return true, nil
	}
	groups, err := game.DB.ListShipGroups(ctx, commanderID)
	if err != nil {
		return false, err
	}
	for _, row := range groups {
		if row.GroupID != skin.group {
			continue
		}
		switch skin.skinType {
		case skinTypeDefault:
			return true, nil
		case skinTypePropose:
			return row.MarryFlag > 0, nil
		case skinTypeRemake:
			return row.Trans, nil
		}
	}
	return false, nil
}

// handleJuusSkin sets a ship chat's background skin (InstagramChatCommand
// SET_SKIN, CS_11714); 0 goes back to the ship's painting.
func (game *Game) handleJuusSkin(client *connection.Client, req *protobuf.CS_11714) error {
	ctx := context.Background()
	group, skinID := req.GetGroupId(), req.GetSkinId()
	ok, err := game.juusChatGroup(ctx, client.CommanderID, group)
	if err == nil && ok {
		ok = juusChatConfigs[group].Type == juusChatShip
	}
	if err == nil && ok && skinID != 0 {
		ok, err = game.juusSkinAllowed(ctx, client.CommanderID, group, skinID)
	}
	if err != nil {
		return err
	}
	if !ok {
		return client.Send(&protobuf.SC_11715{Result: result(errRefused)})
	}
	if err := game.DB.SetJuusGroupSkin(ctx, db.SetJuusGroupSkinParams{CommanderID: client.CommanderID, GroupID: group, SkinID: skinID}); err != nil {
		return err
	}
	return client.Send(&protobuf.SC_11715{Result: result(nil)})
}

// handleJuusCare sets a chat's care flag (InstagramChatCommand SET_CARE,
// CS_11716): 1 cared for, 0 not, as the care button toggles it.
func (game *Game) handleJuusCare(client *connection.Client, req *protobuf.CS_11716) error {
	ctx := context.Background()
	ok, err := game.juusChatGroup(ctx, client.CommanderID, req.GetGroupId())
	if err != nil {
		return err
	}
	if !ok || req.GetValue() > 1 {
		return client.Send(&protobuf.SC_11717{Result: result(errRefused)})
	}
	if err := game.DB.SetJuusGroupFavorite(ctx, db.SetJuusGroupFavoriteParams{CommanderID: client.CommanderID, GroupID: req.GetGroupId(), Favorite: req.GetValue()}); err != nil {
		return err
	}
	return client.Send(&protobuf.SC_11717{Result: result(nil)})
}

// handleJuusTopic sets the topic a chat shows (InstagramChatCommand
// SET_TOPIC, CS_11718): one of its topics the commander unlocked (the topic
// picker only lists the active ones).
func (game *Game) handleJuusTopic(client *connection.Client, req *protobuf.CS_11718) error {
	ctx := context.Background()
	id := req.GetChatGroupId()
	topics, err := game.DB.ListJuusTopics(ctx, client.CommanderID)
	if err != nil {
		return err
	}
	config, ok := juusTopicConfigs[id]
	unlocked := slices.ContainsFunc(topics, func(topic db.ListJuusTopicsRow) bool { return topic.TopicID == id })
	if !ok || !unlocked {
		return client.Send(&protobuf.SC_11719{Result: result(errRefused)})
	}
	if err := game.DB.SetJuusGroupTopic(ctx, db.SetJuusGroupTopicParams{CommanderID: client.CommanderID, GroupID: config.ShipGroup, CurTopic: id}); err != nil {
		return err
	}
	return client.Send(&protobuf.SC_11719{Result: result(nil)})
}

// handleJuusGroups answers InstagramChatCommand with the commander's Juus
// chats (SC_11711).
func (game *Game) handleJuusGroups(client *connection.Client, req *protobuf.CS_11710) error {
	groups, err := game.juusGroups(context.Background(), client.CommanderID)
	if err != nil {
		return err
	}
	return client.Send(groups)
}

// handleJuusActivateTopics unlocks chat topics (InstagramChatCommand
// ACTIVATE_TOPIC): one result per topic, 0 when it exists, and the unlock
// time.
func (game *Game) handleJuusActivateTopics(client *connection.Client, req *protobuf.CS_11722) error {
	ctx := context.Background()
	now := uint32(time.Now().Unix())
	reply := &protobuf.SC_11723{OpTime: proto.Uint32(now)}
	for _, id := range req.GetChatGroupIdList() {
		if _, ok := juusTopicConfigs[id]; !ok {
			reply.ResultList = append(reply.ResultList, *result(errRefused))
			continue
		}
		if _, err := game.DB.ActivateJuusTopic(ctx, db.ActivateJuusTopicParams{CommanderID: client.CommanderID, TopicID: id, OpTime: now}); err != nil {
			return err
		}
		reply.ResultList = append(reply.ResultList, *result(nil))
	}
	return client.Send(reply)
}

// handleJuusChatReply records the commander's answer in a chat topic
// (InstagramChatCommand REPLY: chat_id is the line answered, value the
// option); opening a red packet line gives its activity_ins_redpackage
// content.
func (game *Game) handleJuusChatReply(client *connection.Client, req *protobuf.CS_11712) error {
	ctx := context.Background()
	now := uint32(time.Now().Unix())
	reply := &protobuf.SC_11713{Result: result(nil), OpTime: proto.Uint32(now)}
	err := game.tx(ctx, client, func(tx *Tx) error {
		touched, err := tx.TouchJuusTopic(ctx, db.TouchJuusTopicParams{OpTime: now, CommanderID: client.CommanderID, TopicID: req.GetChatGroupId()})
		if err != nil {
			return err
		}
		if touched == 0 {
			reply.Result = result(errRefused)
			return nil
		}
		if err := tx.AddJuusReply(ctx, db.AddJuusReplyParams{CommanderID: client.CommanderID, TopicID: req.GetChatGroupId(), ChatID: req.GetChatId(), Value: req.GetValue()}); err != nil {
			return err
		}
		drop, ok := juusRedPackets[req.GetChatId()]
		if !ok {
			return nil
		}
		reply.DropList = configDrops([][]uint32{drop})
		return tx.grantDrops(reply.DropList)
	})
	if err != nil {
		return err
	}
	return client.Send(reply)
}

// handleJuusTopicsRead marks chat topics read (InstagramChatCommand
// SET_READTIP), all of them when the list is empty.
func (game *Game) handleJuusTopicsRead(client *connection.Client, req *protobuf.CS_11720) error {
	ctx := context.Background()
	if len(req.GetChatGroupIdList()) == 0 {
		if err := game.DB.ReadAllJuusTopics(ctx, client.CommanderID); err != nil {
			return err
		}
	}
	for _, id := range req.GetChatGroupIdList() {
		if err := game.DB.ReadJuusTopic(ctx, db.ReadJuusTopicParams{CommanderID: client.CommanderID, TopicID: id}); err != nil {
			return err
		}
	}
	return client.Send(&protobuf.SC_11721{Result: result(nil)})
}
