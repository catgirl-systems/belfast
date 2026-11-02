package game

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/region"
)

// The back-office's edits of a commander's Archives (internal/admin
// archives.go): the ship archive (CollectionProxy's ship groups), the
// collection milestones (storeup_data_template), the medal tiers
// (medal_template) and the medals shown, the frames and wallpapers owned
// and worn (AttireProxy, LivingAreaCoverProxy), the memories seen
// (memory_group / memory_template over the stories played) and the main
// scene's music player. Every write kicks the commander first and runs in
// one transaction (AdminWrite).

// --- ship archive

// ArchiveEntry is an archive entry (SHIP_STATISTICS_INFO) with the bounds
// an edit must keep: never under what the ships the commander owns give it
// (shipGroupBest, CollectionProxy.flushCollection would raise it back on
// the client), never past what the group can reach.
type ArchiveEntry struct {
	Group    uint32 `json:"group"`
	Level    uint32 `json:"level"`
	Intimacy uint32 `json:"intimacy"`
	Star     uint32 `json:"star"`
	Married  bool   `json:"married"`
	Liked    bool   `json:"liked"`
	Retrofit bool   `json:"retrofit"`
	// the best ship of the group the commander owns, 0 none
	Ship uint32 `json:"ship"`
	// the lowest values the owned ships allow
	MinLevel    uint32 `json:"min_level"`
	MinIntimacy uint32 `json:"min_intimacy"`
	MinStar     uint32 `json:"min_star"`
	MinMarried  bool   `json:"min_married"`
	// the highest: the ship config's top level, the affinity cap (oathed
	// or not), the group's best template star
	MaxLevel        uint32 `json:"max_level"`
	MaxIntimacy     uint32 `json:"max_intimacy"`
	MaxIntimacyOath uint32 `json:"max_intimacy_oath"`
	MaxStar         uint32 `json:"max_star"`
	// the group has a retrofit that changes its look (transform_data_template
	// skin_id), what sets SC_17001 transform_list
	CanRetrofit bool `json:"can_retrofit"`
}

// groupStars are each archive group's base star (its template group*10+1,
// what archiveStar starts from) and its best template star.
var groupStars = sync.OnceValue(func() map[uint32][2]uint32 {
	stars := map[uint32][2]uint32{}
	for id, template := range ownableShips {
		group := shipGroup(id)
		if !archiveGroups[group] {
			continue
		}
		row := stars[group]
		row[1] = max(row[1], template.Star)
		stars[group] = row
	}
	for group, row := range stars {
		row[0] = shipTemplates[group*10+1].Star
		row[1] = max(row[1], row[0])
		stars[group] = row
	}
	return stars
})

// groupRetrofits tells whether a ship group's retrofit gives it a new look.
func groupRetrofits(group uint32) bool {
	for _, id := range transformGroups[group] {
		if transforms[id].SkinID != 0 {
			return true
		}
	}
	return false
}

// ArchiveGroups are the ship groups an archive entry can be made of
// (ship_data_group), in id order.
func ArchiveGroups() []uint32 {
	return slices.Sorted(maps.Keys(archiveGroups))
}

// ownedBest is what the commander's ships give each group: the
// shipGroupBest values, and the best ship (highest level).
type ownedBest struct {
	best db.RaiseShipGroupParams
	ship db.Ship
}

func ownedGroups(ctx context.Context, queries *db.Queries, commanderID uint32) (map[uint32]ownedBest, error) {
	ships, err := queries.ListShipsByCommander(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	owned := map[uint32]ownedBest{}
	for _, ship := range ships {
		best, ok := shipGroupBest(ship)
		if !ok {
			continue
		}
		row, seen := owned[best.GroupID]
		if !seen {
			row = ownedBest{best: best, ship: ship}
		} else {
			row.best.Star = max(row.best.Star, best.Star)
			row.best.IntimacyMax = max(row.best.IntimacyMax, best.IntimacyMax)
			row.best.LvMax = max(row.best.LvMax, best.LvMax)
			row.best.MarryFlag = max(row.best.MarryFlag, best.MarryFlag)
			if ship.Level > row.ship.Level {
				row.ship = ship
			}
		}
		owned[best.GroupID] = row
	}
	return owned, nil
}

// oathIntimacyCap is the last intimacy_template upper_bound, the affinity an
// oathed ship stops at (Ship:addLikability).
func oathIntimacyCap() uint32 {
	if len(intimacies) == 0 {
		return 2 * maxIntimacy
	}
	return intimacies[len(intimacies)-1].UpperBound
}

func archiveEntry(group db.CommanderShipGroup, owned map[uint32]ownedBest) ArchiveEntry {
	stars := groupStars()[group.GroupID]
	entry := ArchiveEntry{
		Group: group.GroupID, Level: group.LvMax, Intimacy: group.IntimacyMax, Star: archiveStar(group),
		Married: group.MarryFlag != 0, Liked: group.HeartFlag != 0, Retrofit: group.Trans,
		MinLevel: 1, MinStar: stars[0],
		MaxLevel: max(shipConfigMaxLevel, group.LvMax), MaxIntimacy: maxIntimacy, MaxIntimacyOath: oathIntimacyCap(), MaxStar: max(stars[1], archiveStar(group)),
		CanRetrofit: groupRetrofits(group.GroupID) || group.Trans,
	}
	if best, ok := owned[group.GroupID]; ok {
		entry.Ship = best.ship.ID
		entry.MinLevel = max(entry.MinLevel, best.best.LvMax)
		entry.MinIntimacy = best.best.IntimacyMax
		entry.MinStar = max(entry.MinStar, best.best.Star)
		entry.MinMarried = best.best.MarryFlag != 0
	}
	return entry
}

// AdminArchiveEntries lists the commander's archive entries with their
// bounds.
func (game *Game) AdminArchiveEntries(ctx context.Context, commanderID uint32) ([]ArchiveEntry, error) {
	q := game.DB.Queries
	groups, err := q.ListShipGroups(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	owned, err := ownedGroups(ctx, q, commanderID)
	if err != nil {
		return nil, err
	}
	list := make([]ArchiveEntry, 0, len(groups))
	for _, group := range groups {
		if archiveGroups[group.GroupID] {
			list = append(list, archiveEntry(group, owned))
		}
	}
	return list, nil
}

// setArchiveCount stores the archive medal's progress, the entries counted
// (recordShipGroup).
func (tx *Tx) setArchiveCount() error {
	groups, err := tx.CountShipGroups(tx.ctx, tx.commanderID)
	if err != nil {
		return err
	}
	return tx.SetTrophy(tx.ctx, db.SetTrophyParams{CommanderID: tx.commanderID, TrophyID: archiveCountTrophy, Progress: uint32(groups)})
}

// AddArchiveEntry adds a ship group to the archives as a ship of it new
// from the shipyard would (recordShipGroup): its base star, level 1, the
// starting affinity.
func (game *Game) AddArchiveEntry(ctx context.Context, commanderID, group uint32) (bool, error) {
	if !archiveGroups[group] {
		return false, adminRefused("%d isn't a ship group of the archives", group)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		added, err := tx.AddShipGroup(ctx, db.AddShipGroupParams{
			CommanderID: commanderID, GroupID: group, IntimacyMax: newShipIntimacy, LvMax: 1, Star: groupStars()[group][0],
		})
		if err != nil {
			return err
		}
		if added == 0 {
			return AdminError{Msg: "the ship is already in the archives"}
		}
		tx.techPointsChanged()
		return tx.setArchiveCount()
	})
}

// RemoveArchiveEntry takes a ship group out of the archives, when the
// commander owns no ship of it (the client would add it back).
func (game *Game) RemoveArchiveEntry(ctx context.Context, commanderID, group uint32) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		owned, err := ownedGroups(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		if _, ok := owned[group]; ok {
			return AdminError{Msg: "the commander owns a ship of this group"}
		}
		removed, err := tx.ArchivesDeleteShipGroup(ctx, db.ArchivesDeleteShipGroupParams{CommanderID: commanderID, GroupID: group})
		if err != nil {
			return err
		}
		if removed == 0 {
			return AdminError{Msg: "the ship isn't in the archives"}
		}
		tx.techPointsChanged()
		return tx.setArchiveCount()
	})
}

// ArchiveEdit is what an archive entry edit sets.
type ArchiveEdit struct {
	Level    uint32 `json:"level"`
	Intimacy uint32 `json:"intimacy"`
	Star     uint32 `json:"star"`
	Married  bool   `json:"married"`
	Liked    bool   `json:"liked"`
	Retrofit bool   `json:"retrofit"`
}

// SetArchiveEntry sets an archive entry's best level, affinity, star, oath,
// like (LikeShipCommand's heart_flag) and retrofit, within its bounds.
func (game *Game) SetArchiveEntry(ctx context.Context, commanderID, group uint32, edit ArchiveEdit) (bool, error) {
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		groups, err := tx.ListShipGroups(ctx, commanderID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(groups, func(row db.CommanderShipGroup) bool { return row.GroupID == group })
		if i < 0 || !archiveGroups[group] {
			return AdminError{Msg: "the ship isn't in the archives"}
		}
		owned, err := ownedGroups(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		entry := archiveEntry(groups[i], owned)
		intimacyCap := entry.MaxIntimacy
		if edit.Married {
			intimacyCap = entry.MaxIntimacyOath
		}
		switch {
		case edit.Level < entry.MinLevel || edit.Level > entry.MaxLevel:
			return adminRefused("the level goes from %d to %d", entry.MinLevel, entry.MaxLevel)
		case edit.Intimacy < entry.MinIntimacy || edit.Intimacy > intimacyCap:
			return adminRefused("the affinity goes from %d to %d", entry.MinIntimacy, intimacyCap)
		case edit.Star < entry.MinStar || edit.Star > entry.MaxStar:
			return adminRefused("the stars go from %d to %d", entry.MinStar, entry.MaxStar)
		case entry.MinMarried && !edit.Married:
			return AdminError{Msg: "an owned ship of the group is oathed"}
		case edit.Retrofit && !entry.CanRetrofit:
			return AdminError{Msg: "the group has no retrofit"}
		}
		values := db.ArchivesSetShipGroupParams{
			LvMax: edit.Level, IntimacyMax: edit.Intimacy, Star: edit.Star, Trans: edit.Retrofit,
			CommanderID: commanderID, GroupID: group,
		}
		if edit.Married {
			values.MarryFlag = 1
		}
		if edit.Liked {
			values.HeartFlag = 1
		}
		if _, err := tx.ArchivesSetShipGroup(ctx, values); err != nil {
			return err
		}
		tx.techPointsChanged()
		return nil
	})
}

// --- collection milestones

// ArchiveCollection is a storeup_data_template milestone: the stars its
// ship groups add up to in the archives (Favorite.getStarCount), each
// award's level and how many are claimed (always the first ones,
// Favorite.getNextAwardIndex).
type ArchiveCollection struct {
	ID      uint32      `json:"id"`
	Stars   uint32      `json:"stars"`
	Levels  []uint32    `json:"levels"`
	Awards  [][3]uint32 `json:"-"`
	Claimed int         `json:"claimed"`
}

// AdminArchiveCollections lists the milestones in id order.
func (game *Game) AdminArchiveCollections(ctx context.Context, commanderID uint32) ([]ArchiveCollection, error) {
	q := game.DB.Queries
	groups, err := q.ListShipGroups(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	stars := map[uint32]uint32{}
	for _, group := range groups {
		stars[group.GroupID] = archiveStar(group)
	}
	awards, err := q.ListCollectionAwards(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	claimed := map[uint32]int{}
	for _, award := range awards {
		claimed[award.StoreupID] = max(claimed[award.StoreupID], int(award.AwardIndex))
	}
	var list []ArchiveCollection
	for _, id := range slices.Sorted(maps.Keys(storeupConfigs)) {
		config := storeupConfigs[id]
		row := ArchiveCollection{ID: id, Levels: config.Level, Claimed: claimed[id]}
		for _, group := range config.CharList {
			row.Stars += stars[group]
		}
		for _, award := range config.AwardDisplay {
			if len(award) >= 3 {
				row.Awards = append(row.Awards, [3]uint32{award[0], award[1], award[2]})
			} else {
				row.Awards = append(row.Awards, [3]uint32{})
			}
		}
		list = append(list, row)
	}
	return list, nil
}

// SetCollectionClaimed marks a milestone's awards claimed up to index
// (1-based), each once its level is reached as CollectionGetAwardCommand
// checks (Favorite.canGetRes), or unclaimed from index on. Nothing is
// given or taken back.
func (game *Game) SetCollectionClaimed(ctx context.Context, commanderID, id, index uint32, claimed bool) (bool, error) {
	config, ok := storeupConfigs[id]
	if !ok || index == 0 || int(index) > len(config.Level) || int(index) > len(config.AwardDisplay) {
		return false, adminRefused("no award %d in milestone %d", index, id)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		if !claimed {
			return tx.ArchivesUnclaimCollection(ctx, db.ArchivesUnclaimCollectionParams{CommanderID: commanderID, StoreupID: id, AwardIndex: index})
		}
		groups, err := tx.ListShipGroups(ctx, commanderID)
		if err != nil {
			return err
		}
		stars := uint32(0)
		for _, group := range groups {
			if slices.Contains(config.CharList, group.GroupID) {
				stars += archiveStar(group)
			}
		}
		if stars < config.Level[index-1] {
			return adminRefused("it needs %d stars, the archives have %d", config.Level[index-1], stars)
		}
		for i := uint32(1); i <= index; i++ {
			if _, err := tx.AddCollectionAward(ctx, db.AddCollectionAwardParams{CommanderID: commanderID, StoreupID: id, AwardIndex: i}); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- medals

// ArchiveMedalTier is a medal tier: its target, the progress its metric
// shows, whether it is claimed and whether it could be (tierReached).
type ArchiveMedalTier struct {
	ID       uint32 `json:"id"`
	Target   uint32 `json:"target"`
	Progress uint32 `json:"progress"`
	Claimed  bool   `json:"claimed"`
	Reached  bool   `json:"reached"`
}

// ArchiveMedal is a medal (its first tier's id) and its tiers; Hidden is a
// medal never shown on a profile (Trophy.ALWAYS_HIDE).
type ArchiveMedal struct {
	ID     uint32             `json:"id"`
	Hidden bool               `json:"hidden"`
	Tiers  []ArchiveMedalTier `json:"tiers"`
}

// medalTiers are a medal's tiers in order, the Coming Soon ones left out.
func medalTiers(id uint32) []uint32 {
	var tiers []uint32
	for tier := id; tier != 0; tier = medalTemplates[tier].Next {
		config, ok := medalTemplates[tier]
		if !ok || slices.Contains(tiers, tier) || config.TargetType == medalTargetComingSoon {
			break
		}
		tiers = append(tiers, tier)
	}
	return tiers
}

// tierReached tells whether a tier's target is met: its metric's progress
// for most, for a complex medal its sub-medals won (claimed, or reached:
// CollectionProxy:hiddenTrophyAutoClaim claims them as soon as they are).
func (state trophyState) tierReached(tier uint32) bool {
	if medalTemplates[tier].TargetType == medalTargetComingSoon {
		return false
	}
	return state.tierProgress(tier) >= medalTemplates[tier].TargetNum
}

// tierProgress is what a tier counts: its metric's progress, or for a
// complex medal its sub-medals won (Trophy:getProgress).
func (state trophyState) tierProgress(tier uint32) uint32 {
	if medalTemplates[tier].TargetType != medalTargetSubMedals {
		return state.info(tier).GetProgress()
	}
	won := uint32(0)
	for _, sub := range trophySubMedals(tier) {
		if state.claimed[sub] != 0 || state.info(sub).GetProgress() >= medalTemplates[sub].TargetNum {
			won++
		}
	}
	return won
}

// AdminArchiveMedals lists the medals with progress or a tier claimed.
// A complex medal's progress is its sub-medals won.
func (game *Game) AdminArchiveMedals(ctx context.Context, commanderID uint32) ([]ArchiveMedal, error) {
	state, err := loadTrophyState(ctx, game.DB.Queries, commanderID)
	if err != nil {
		return nil, err
	}
	var list []ArchiveMedal
	for _, id := range archiveTrophies {
		if medalTemplates[id].Start != 0 {
			continue
		}
		medal := ArchiveMedal{ID: id, Hidden: medalTemplates[id].Hide == medalAlwaysHidden}
		active := false
		for _, tier := range medalTiers(id) {
			row := ArchiveMedalTier{
				ID: tier, Target: medalTemplates[tier].TargetNum, Progress: state.tierProgress(tier),
				Claimed: state.claimed[tier] != 0, Reached: state.tierReached(tier),
			}
			active = active || row.Claimed || row.Progress > 0
			medal.Tiers = append(medal.Tiers, row)
		}
		if active {
			list = append(list, medal)
		}
	}
	return list, nil
}

// SetMedalClaimed claims a medal's tiers up to tier, each reached (the
// sub-medals of a complex one with it), as TrophyClaimCommand would one
// after the other, or unclaims tier and the ones after it (with their
// sub-medals), taking them off the profile.
func (game *Game) SetMedalClaimed(ctx context.Context, commanderID, tier uint32, claimed bool) (bool, error) {
	var tiers []uint32
	at := -1
	for _, id := range archiveTrophies {
		if medalTemplates[id].Start != 0 {
			continue
		}
		if list := medalTiers(id); slices.Contains(list, tier) {
			tiers, at = list, slices.Index(list, tier)
			break
		}
	}
	if at < 0 {
		return false, adminRefused("%d isn't a medal tier", tier)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		if !claimed {
			for _, id := range tiers[at:] {
				for _, each := range append([]uint32{id}, trophySubMedals(id)...) {
					if err := tx.LifeDeleteTrophyClaim(ctx, db.LifeDeleteTrophyClaimParams{CommanderID: commanderID, TrophyID: each}); err != nil {
						return err
					}
					if err := tx.LifeDeleteDisplayMedal(ctx, db.LifeDeleteDisplayMedalParams{CommanderID: commanderID, MedalID: each}); err != nil {
						return err
					}
				}
			}
			return nil
		}
		state, err := loadTrophyState(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		now := serverNow().Unix()
		claim := func(id uint32) error {
			if state.claimed[id] != 0 {
				return nil
			}
			state.claimed[id] = now
			_, err := tx.ClaimTrophy(ctx, db.ClaimTrophyParams{CommanderID: commanderID, TrophyID: id, ClaimedAt: now})
			return err
		}
		for _, id := range tiers[:at+1] {
			if state.claimed[id] != 0 {
				continue
			}
			if !state.tierReached(id) {
				return adminRefused("tier %d isn't reached", id)
			}
			for _, sub := range trophySubMedals(id) {
				if state.info(sub).GetProgress() >= medalTemplates[sub].TargetNum {
					if err := claim(sub); err != nil {
						return err
					}
				}
			}
			if err := claim(id); err != nil {
				return err
			}
		}
		return nil
	})
}

// AdminDisplayMedals is the medals the commander shows, in order.
func (game *Game) AdminDisplayMedals(ctx context.Context, commanderID uint32) ([]uint32, error) {
	return game.DB.ListDisplayMedals(ctx, commanderID)
}

// MaxDisplayMedals is how many medals a profile shows.
const MaxDisplayMedals = maxDisplayMedals

// SetDisplayMedals sets the medals shown, checked as ChangeMedalDisplayCommand
// is (handleChangeMedalDisplay): at most 5, each once, claimed medal tiers
// that aren't always hidden or love letter medals the commander has.
func (game *Game) SetDisplayMedals(ctx context.Context, commanderID uint32, ids []uint32) (bool, error) {
	if len(ids) > maxDisplayMedals {
		return false, adminRefused("a profile shows %d medals at most", maxDisplayMedals)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		claims, err := tx.ListTrophyClaims(ctx, commanderID)
		if err != nil {
			return err
		}
		claimed := map[uint32]bool{}
		for _, claim := range claims {
			claimed[claim.TrophyID] = true
		}
		letters, err := tx.ListLoveLetterMedals(ctx, commanderID)
		if err != nil {
			return err
		}
		letterLevels := map[uint32]uint32{}
		for _, letter := range letters {
			letterLevels[letter.GroupID] = letter.Level
		}
		seen := map[uint32]bool{}
		for _, id := range ids {
			if seen[id] {
				return adminRefused("medal %d twice", id)
			}
			seen[id] = true
			if id > loveLetterMedalBase {
				group, level := (id-loveLetterMedalBase)/100, (id-loveLetterMedalBase)%100
				if level%10 != 1 || level > letterLevels[group] {
					return adminRefused("love letter medal %d isn't the commander's", id)
				}
			} else if !claimed[id] || medalTemplates[id].Hide == medalAlwaysHidden {
				return adminRefused("medal %d isn't claimed", id)
			}
		}
		if err := tx.ClearDisplayMedals(ctx, commanderID); err != nil {
			return err
		}
		for i, id := range ids {
			if err := tx.AddDisplayMedal(ctx, db.AddDisplayMedalParams{CommanderID: commanderID, Position: uint32(i), MedalID: id}); err != nil {
				return err
			}
		}
		return nil
	})
}

// --- frames and wallpapers

// The kinds of cosmetics the Archives grant and remove: the icon frames,
// chat frames and port wallpapers (their drop types).
const (
	ArchiveIconFrame = dropTypeIconFrame
	ArchiveChatFrame = dropTypeChatFrame
	ArchiveCover     = dropTypeCover
)

// ArchiveFrame is a frame or wallpaper the commander owns.
type ArchiveFrame struct {
	Type uint32 `json:"type"`
	ID   uint32 `json:"id"`
	Worn bool   `json:"worn"`
}

func attireTable(kind uint32) map[uint32]struct{} {
	switch kind {
	case dropTypeIconFrame:
		return iconFrames
	case dropTypeChatFrame:
		return chatFrames
	case dropTypeCover:
		return covers
	}
	return nil
}

// ArchiveFrameCatalog is every icon frame, chat frame and wallpaper id
// there is (the default 0 aside), by kind.
func ArchiveFrameCatalog() map[uint32][]uint32 {
	catalog := map[uint32][]uint32{}
	for _, kind := range []uint32{dropTypeIconFrame, dropTypeChatFrame, dropTypeCover} {
		for id := range attireTable(kind) {
			if id != defaultAppearance {
				catalog[kind] = append(catalog[kind], id)
			}
		}
		slices.Sort(catalog[kind])
	}
	return catalog
}

// AdminArchiveFrames lists the frames and wallpapers owned, those worn
// marked.
func (game *Game) AdminArchiveFrames(ctx context.Context, commanderID uint32) ([]ArchiveFrame, error) {
	q := game.DB.Queries
	rows, err := q.ListAttireFrames(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	shown, err := appearance(ctx, q, commanderID)
	if err != nil {
		return nil, err
	}
	worn := map[uint32]uint32{dropTypeIconFrame: shown.IconFrame, dropTypeChatFrame: shown.ChatFrame, dropTypeCover: shown.Cover}
	list := []ArchiveFrame{}
	for _, row := range rows {
		if attireTable(row.DropType) == nil {
			continue
		}
		list = append(list, ArchiveFrame{Type: row.DropType, ID: row.FrameID, Worn: worn[row.DropType] == row.FrameID})
	}
	return list, nil
}

func knownAttire(kind, id uint32) error {
	table := attireTable(kind)
	if table == nil {
		return adminRefused("drop type %d isn't a frame or wallpaper", kind)
	}
	if _, ok := table[id]; !ok && id != defaultAppearance {
		return adminRefused("no frame or wallpaper %d", id)
	}
	return nil
}

// AddArchiveFrame gives the commander a frame or wallpaper for good, as its
// drop does (addAttireFrame).
func (game *Game) AddArchiveFrame(ctx context.Context, commanderID, kind, id uint32) (bool, error) {
	if err := knownAttire(kind, id); err != nil {
		return false, err
	}
	if id == defaultAppearance {
		return false, AdminError{Msg: "everyone has the default"}
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.addAttireFrame(kind, id)
	})
}

// wearAttire sets the frame or wallpaper worn.
func (tx *Tx) wearAttire(kind, id uint32) error {
	switch kind {
	case dropTypeIconFrame:
		return tx.SetIconFrame(tx.ctx, db.SetIconFrameParams{CommanderID: tx.commanderID, IconFrame: id})
	case dropTypeChatFrame:
		return tx.SetChatFrame(tx.ctx, db.SetChatFrameParams{CommanderID: tx.commanderID, ChatFrame: id})
	}
	return tx.SetCover(tx.ctx, db.SetCoverParams{CommanderID: tx.commanderID, Cover: id})
}

// RemoveArchiveFrame takes a frame or wallpaper back; one worn goes back to
// the default.
func (game *Game) RemoveArchiveFrame(ctx context.Context, commanderID, kind, id uint32) (bool, error) {
	if err := knownAttire(kind, id); err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		removed, err := tx.ArchivesDeleteAttireFrame(ctx, db.ArchivesDeleteAttireFrameParams{CommanderID: commanderID, DropType: kind, FrameID: id})
		if err != nil {
			return err
		}
		if removed == 0 {
			return AdminError{Msg: "the commander doesn't own it"}
		}
		shown, err := appearance(ctx, tx.Queries, commanderID)
		if err != nil {
			return err
		}
		worn := map[uint32]uint32{dropTypeIconFrame: shown.IconFrame, dropTypeChatFrame: shown.ChatFrame, dropTypeCover: shown.Cover}
		if worn[kind] == id {
			return tx.wearAttire(kind, defaultAppearance)
		}
		return nil
	})
}

// WearArchiveFrame sets the frame or wallpaper worn, one the commander
// owns (CS_11005, CS_11030), 0 the default.
func (game *Game) WearArchiveFrame(ctx context.Context, commanderID, kind, id uint32) (bool, error) {
	if err := knownAttire(kind, id); err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		owned, err := ownsAttire(ctx, tx.Queries, commanderID, kind, id)
		if err != nil {
			return err
		}
		if !owned {
			return AdminError{Msg: "the commander doesn't own it"}
		}
		return tx.wearAttire(kind, id)
	})
}

// --- memories

// memoryConfig is a memory_template row: open from the start (is_open 1)
// or once one of the unlock_pre stories is played
// (WorldMediaCollectionMemoryGroupLayer, NewStoryMgr:IsPlayed(name, true)).
type memoryConfig struct {
	Title     string   `json:"title"`
	IsOpen    uint32   `json:"is_open"`
	UnlockPre []string `json:"unlock_pre"`
}

// memoryGroupConfig is a memory_group row, its memories in order.
type memoryGroupConfig struct {
	Title    string   `json:"title"`
	Memories []uint32 `json:"memories"`
}

type memoryData struct {
	templates map[uint32]memoryConfig
	groups    map[uint32]memoryGroupConfig
	// story script name: its id (NewStoryMgr:StoryName2StoryId, the
	// region's GameCfg/story index list, 1-based)
	stories map[string]uint32
}

// storyIndexes are each region's story index (NewStoryMgr's "index" ..
// the platform's code: indexus for EN, index for the Chinese clients).
var storyIndexes = map[string]string{"EN": "indexus", "JP": "indexjp", "KR": "indexkr", "CN": "index", "TW": "index"}

// ErrNoStoryIndex is a data dir without the client's story index
// (GameCfg/story.json): the memories can't be told seen.
var ErrNoStoryIndex = errors.New("no story index in the game data")

var loadMemories = sync.OnceValues(func() (*memoryData, error) {
	data := &memoryData{stories: map[string]uint32{}}
	var err error
	if data.templates, err = gamedata.Table[memoryConfig]("memory_template"); err != nil {
		return nil, err
	}
	if data.groups, err = gamedata.Table[memoryGroupConfig]("memory_group"); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(gamedata.RegionDir(), "GameCfg", "story.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoStoryIndex, err)
	}
	var files map[string]json.RawMessage
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, err
	}
	index, ok := files[storyIndexes[region.Current()]]
	if !ok {
		return nil, ErrNoStoryIndex
	}
	// a Lua list: an object keyed by position, or an array
	var byKey map[string]string
	if json.Unmarshal(index, &byKey) == nil {
		for key, name := range byKey {
			if id, err := strconv.ParseUint(key, 10, 32); err == nil {
				data.stories[name] = uint32(id)
			}
		}
	} else {
		var list []*string
		if err := json.Unmarshal(index, &list); err != nil {
			return nil, err
		}
		for i, name := range list {
			if name != nil {
				data.stories[*name] = uint32(i + 1)
			}
		}
	}
	return data, nil
})

// ArchiveMemory is a memory: open from the start, or seen once one of its
// stories is played.
type ArchiveMemory struct {
	ID    uint32 `json:"id"`
	Title string `json:"title"`
	Open  bool   `json:"open"`
	Seen  bool   `json:"seen"`
}

// ArchiveMemoryGroup is a memory_group with its memories seen.
type ArchiveMemoryGroup struct {
	ID       uint32          `json:"id"`
	Title    string          `json:"title"`
	Seen     int             `json:"seen"`
	Total    int             `json:"total"`
	Memories []ArchiveMemory `json:"memories,omitempty"`
}

func (data *memoryData) memory(id uint32, played map[uint32]bool) ArchiveMemory {
	config := data.templates[id]
	memory := ArchiveMemory{ID: id, Title: strings.TrimSpace(config.Title), Open: config.IsOpen == 1}
	memory.Seen = memory.Open
	for _, name := range config.UnlockPre {
		if story, ok := data.stories[name]; ok && played[story] {
			memory.Seen = true
		}
	}
	return memory
}

func playedStories(ctx context.Context, queries *db.Queries, commanderID uint32) (map[uint32]bool, error) {
	stories, err := queries.ListStories(ctx, commanderID)
	if err != nil {
		return nil, err
	}
	played := make(map[uint32]bool, len(stories))
	for _, story := range stories {
		played[story] = true
	}
	return played, nil
}

// AdminMemoryGroups lists the memory groups with their memories seen, the
// memories themselves for group (0 none).
func (game *Game) AdminMemoryGroups(ctx context.Context, commanderID, group uint32) ([]ArchiveMemoryGroup, error) {
	data, err := loadMemories()
	if err != nil {
		return nil, err
	}
	played, err := playedStories(ctx, game.DB.Queries, commanderID)
	if err != nil {
		return nil, err
	}
	var list []ArchiveMemoryGroup
	for _, id := range slices.Sorted(maps.Keys(data.groups)) {
		config := data.groups[id]
		row := ArchiveMemoryGroup{ID: id, Title: strings.TrimSpace(config.Title)}
		for _, memoryID := range config.Memories {
			if _, ok := data.templates[memoryID]; !ok {
				continue
			}
			memory := data.memory(memoryID, played)
			row.Total++
			if memory.Seen {
				row.Seen++
			}
			if id == group {
				row.Memories = append(row.Memories, memory)
			}
		}
		if row.Total > 0 {
			list = append(list, row)
		}
	}
	return list, nil
}

// SetMemoriesSeen marks a memory (or every memory of group, memory 0) seen,
// its first unlock_pre story played, or unseen, none of them played (they
// play again in the game). Memories open from the start stay seen.
func (game *Game) SetMemoriesSeen(ctx context.Context, commanderID, group, memory uint32, seen bool) (bool, error) {
	data, err := loadMemories()
	if errors.Is(err, ErrNoStoryIndex) {
		return false, AdminError{Msg: err.Error()}
	}
	if err != nil {
		return false, err
	}
	config, ok := data.groups[group]
	if !ok {
		return false, adminRefused("no memory group %d", group)
	}
	ids := config.Memories
	if memory != 0 {
		if !slices.Contains(ids, memory) {
			return false, adminRefused("memory %d isn't in group %d", memory, group)
		}
		ids = []uint32{memory}
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.setMemoriesSeen(data, ids, seen)
	})
}

// SetAllMemoriesSeen marks every memory of every group seen, in one
// transaction.
func (game *Game) SetAllMemoriesSeen(ctx context.Context, commanderID uint32) (bool, error) {
	data, err := loadMemories()
	if errors.Is(err, ErrNoStoryIndex) {
		return false, AdminError{Msg: err.Error()}
	}
	if err != nil {
		return false, err
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		for _, id := range slices.Sorted(maps.Keys(data.groups)) {
			if err := tx.setMemoriesSeen(data, data.groups[id].Memories, true); err != nil {
				return err
			}
		}
		return nil
	})
}

// setMemoriesSeen marks the memories ids seen, their first unlock_pre story
// played, or unseen, none of them played.
func (tx *Tx) setMemoriesSeen(data *memoryData, ids []uint32, seen bool) error {
	for _, id := range ids {
		template, ok := data.templates[id]
		if !ok || template.IsOpen == 1 {
			continue
		}
		for _, name := range template.UnlockPre {
			story, ok := data.stories[name]
			if !ok {
				continue
			}
			if seen {
				if err := tx.AddStory(tx.ctx, db.AddStoryParams{CommanderID: tx.commanderID, StoryID: story}); err != nil {
					return err
				}
				break
			}
			if err := tx.ArchivesDeleteStory(tx.ctx, db.ArchivesDeleteStoryParams{CommanderID: tx.commanderID, StoryID: story}); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- music player

// ArchiveAlbum is a music_album the main scene's player can play: its
// first song (what CS_17513 music_no names).
type ArchiveAlbum struct {
	ID    uint32 `json:"id"`
	Album uint32 `json:"album"`
}

// MusicAlbums lists each album_id of music_collect_config with its first
// song, in album order.
func MusicAlbums() []ArchiveAlbum {
	first := map[uint32]uint32{}
	albums, err := gamedata.Table[struct {
		Album uint32 `json:"album_id"`
	}]("music_collect_config")
	if err != nil {
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(albums)) {
		if _, ok := first[albums[id].Album]; !ok {
			first[albums[id].Album] = id
		}
	}
	list := []ArchiveAlbum{}
	for _, album := range slices.Sorted(maps.Keys(first)) {
		list = append(list, ArchiveAlbum{ID: first[album], Album: album})
	}
	return list
}

// MusicModes is how many loop types the player has.
const MusicModes = musicModes

// SetMusicPlayer sets the main scene's player as CS_17513 does: the album
// (0 none, 999 the liked songs, else a song) and the loop type.
func (game *Game) SetMusicPlayer(ctx context.Context, commanderID, no, mode uint32) (bool, error) {
	if _, ok := musicEntries[no]; !ok && no != musicAlbumNone && no != musicAlbumFavor {
		return false, adminRefused("no song %d", no)
	}
	if mode >= musicModes {
		return false, adminRefused("loop type %d: 0 to %d", mode, musicModes-1)
	}
	return game.AdminWrite(ctx, commanderID, func(tx *Tx) error {
		return tx.SetMusicPlayer(ctx, db.SetMusicPlayerParams{CommanderID: commanderID, MusicNo: no, MusicMode: mode})
	})
}
