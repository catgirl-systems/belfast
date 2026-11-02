package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/ggmolly/belfast/internal/game"
	"github.com/ggmolly/belfast/internal/gamedata"
)

// registerArchives adds the back-office's Archives routes: the edits of the
// ship archive, the collection milestones, the medals, the frames and
// wallpapers, the memories and the music player (game/admin_archives.go).
// The page also reads GET /archives and saves the look through
// area_life.go's routes.
func (server *Server) registerArchives(api apiFunc) {
	api("GET /api/commanders/{id}/archives/details", server.handleArchiveDetails)
	api("GET /api/commanders/{id}/archives/memories/{group}", server.handleArchiveMemories)
	api("POST /api/commanders/{id}/archives/ships/{group}", server.otherEdit("group", server.Game.AddArchiveEntry))
	api("DELETE /api/commanders/{id}/archives/ships/{group}", server.otherEdit("group", server.Game.RemoveArchiveEntry))
	api("PUT /api/commanders/{id}/archives/ships/{group}", server.handleArchiveEntry)
	api("POST /api/commanders/{id}/archives/collections/{storeup}/{index}", server.handleArchiveCollection)
	api("POST /api/commanders/{id}/archives/medal-tiers/{tier}", server.handleArchiveMedalTier)
	api("PUT /api/commanders/{id}/archives/medal-display", server.handleArchiveMedalDisplay)
	api("POST /api/commanders/{id}/archives/frames", server.archiveFrame(server.Game.AddArchiveFrame))
	api("POST /api/commanders/{id}/archives/frames/remove", server.archiveFrame(server.Game.RemoveArchiveFrame))
	api("POST /api/commanders/{id}/archives/frames/wear", server.archiveFrame(server.Game.WearArchiveFrame))
	api("POST /api/commanders/{id}/archives/memories/all", server.commanderAction(server.Game.SetAllMemoriesSeen))
	api("POST /api/commanders/{id}/archives/memories/{group}", server.handleArchiveMemoriesSeen)
	api("GET /api/commanders/{id}/archives/cryptolalia", server.handleArchiveCryptolalia)
	api("POST /api/commanders/{id}/archives/cryptolalia/all", server.commanderAction(server.Game.UnlockAllCryptolalia))
	api("POST /api/commanders/{id}/archives/cryptolalia/{story}", server.handleArchiveCryptolaliaStory)
	api("POST /api/commanders/{id}/archives/music", server.handleArchiveMusic)
}

// archiveFrame is a frame or wallpaper edit: {type, id}.
func (server *Server) archiveFrame(edit func(ctx context.Context, id, kind, frame uint32) (bool, error)) func(*http.Request) (any, error) {
	type body struct {
		Type uint32 `json:"type"`
		ID   uint32 `json:"id"`
	}
	return func(r *http.Request) (any, error) {
		return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) { return edit(ctx, id, b.Type, b.ID) })
	}
}

type archiveShip struct {
	game.ArchiveEntry
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Rarity uint32 `json:"rarity"`
}

type archiveGroup struct {
	Group  uint32 `json:"group"`
	Name   string `json:"name"`
	Icon   string `json:"icon,omitempty"`
	Rarity uint32 `json:"rarity"`
}

type archiveStep struct {
	Index   int    `json:"index"`
	Level   uint32 `json:"level"`
	Claimed bool   `json:"claimed"`
	Award   reward `json:"award"`
}

type archiveCollection struct {
	ID      uint32        `json:"id"`
	Name    string        `json:"name"`
	Stars   uint32        `json:"stars"`
	Claimed int           `json:"claimed"`
	Steps   []archiveStep `json:"steps"`
}

type archiveMedal struct {
	game.ArchiveMedal
	Name string `json:"name"`
}

type archiveFrame struct {
	Type uint32 `json:"type"`
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Icon string `json:"icon,omitempty"`
	Worn bool   `json:"worn"`
}

type archiveOption struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

type archiveSkin struct {
	Skin uint32 `json:"skin"`
	Name string `json:"name"`
}

type archiveDetails struct {
	Ships       []archiveShip       `json:"ships"`
	Groups      []archiveGroup      `json:"groups"`
	Collections []archiveCollection `json:"collections"`
	Medals      []archiveMedal      `json:"medals"`
	Display     []uint32            `json:"display"`
	DisplayMax  int                 `json:"display_max"`
	Frames      []archiveFrame      `json:"frames"`
	Catalog     []archiveFrame      `json:"catalog"`
	// the memory groups, nil when the data has no story index
	Memories      []game.ArchiveMemoryGroup `json:"memories"`
	MemoriesError string                    `json:"memories_error,omitempty"`
	Music         struct {
		Album  uint32          `json:"album"`
		Mode   uint32          `json:"mode"`
		Modes  int             `json:"modes"`
		Albums []archiveOption `json:"albums"`
	} `json:"music"`
	// the skins the commander's ships wear, by ship id (none for the default)
	Skins map[uint32]archiveSkin `json:"skins"`
}

// frameNamed is a frame or wallpaper's name and icon (IconFrame.GetIcon,
// LivingAreaCover.GetIcon; chat frames have no texture of their own).
func frameNamed(tables *lifeTables, kind, id uint32) (string, string) {
	var name, icon string
	switch kind {
	case game.ArchiveIconFrame:
		name, icon = tables.iconFrames[id], fmt.Sprintf("iconframe/%d", id)
	case game.ArchiveChatFrame:
		name = tables.chatFrames[id]
	case game.ArchiveCover:
		name, icon = tables.covers[id], tables.coverIcons[id]
	}
	if name = strings.TrimSpace(name); name == "" {
		name = fmt.Sprintf("#%d", id)
	}
	return name, icon
}

// handleArchiveDetails is what the Archives page edits beyond GET /archives:
// the archive entries with their bounds, the groups that can be added, the
// milestones' steps, the medals' tiers and those shown, the frames owned
// and every frame, the memory groups and the music player.
func (server *Server) handleArchiveDetails(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	ctx, id := r.Context(), commander.ID
	tables, err := loadLifeTables()
	if err != nil {
		return nil, err
	}
	reply := archiveDetails{Ships: []archiveShip{}, Groups: []archiveGroup{}, Collections: []archiveCollection{}, Medals: []archiveMedal{}, Frames: []archiveFrame{}, Catalog: []archiveFrame{}, Skins: map[uint32]archiveSkin{}}

	entries, err := server.Game.AdminArchiveEntries(ctx, id)
	if err != nil {
		return nil, err
	}
	present := map[uint32]bool{}
	for _, entry := range entries {
		present[entry.Group] = true
		named := server.shipGroupName(entry.Group)
		reply.Ships = append(reply.Ships, archiveShip{ArchiveEntry: entry, Name: named.Name, Icon: named.Icon, Rarity: named.Rarity})
	}
	for _, group := range game.ArchiveGroups() {
		if present[group] {
			continue
		}
		named := server.shipGroupName(group)
		reply.Groups = append(reply.Groups, archiveGroup{Group: group, Name: named.Name, Icon: named.Icon, Rarity: named.Rarity})
	}

	collections, err := server.Game.AdminArchiveCollections(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, collection := range collections {
		row := archiveCollection{ID: collection.ID, Name: tables.storeups[collection.ID].Name, Stars: collection.Stars, Claimed: collection.Claimed, Steps: []archiveStep{}}
		for i, award := range collection.Awards {
			if i >= len(collection.Levels) {
				break
			}
			row.Steps = append(row.Steps, archiveStep{Index: i + 1, Level: collection.Levels[i], Claimed: i < collection.Claimed, Award: server.namedDrops([][3]uint32{award})[0]})
		}
		reply.Collections = append(reply.Collections, row)
	}

	medals, err := server.Game.AdminArchiveMedals(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, medal := range medals {
		reply.Medals = append(reply.Medals, archiveMedal{ArchiveMedal: medal, Name: strings.TrimSpace(tables.medals[medal.ID].Name)})
	}
	if reply.Display, err = server.Game.AdminDisplayMedals(ctx, id); err != nil {
		return nil, err
	}
	if reply.Display == nil {
		reply.Display = []uint32{}
	}
	reply.DisplayMax = game.MaxDisplayMedals

	frames, err := server.Game.AdminArchiveFrames(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, frame := range frames {
		name, icon := frameNamed(tables, frame.Type, frame.ID)
		reply.Frames = append(reply.Frames, archiveFrame{Type: frame.Type, ID: frame.ID, Name: name, Icon: icon, Worn: frame.Worn})
	}
	catalog := game.ArchiveFrameCatalog()
	for _, kind := range []uint32{game.ArchiveIconFrame, game.ArchiveChatFrame, game.ArchiveCover} {
		for _, frame := range catalog[kind] {
			name, icon := frameNamed(tables, kind, frame)
			reply.Catalog = append(reply.Catalog, archiveFrame{Type: kind, ID: frame, Name: name, Icon: icon})
		}
	}

	if reply.Memories, err = server.Game.AdminMemoryGroups(ctx, id, 0); errors.Is(err, game.ErrNoStoryIndex) {
		reply.MemoriesError, reply.Memories = err.Error(), nil
	} else if err != nil {
		return nil, err
	}

	look, err := server.Game.AdminGetLook(ctx, id)
	if err != nil {
		return nil, err
	}
	reply.Music.Album, reply.Music.Mode, reply.Music.Modes = look.MusicNo, look.MusicM, game.MusicModes
	reply.Music.Albums = []archiveOption{}
	albums, err := loadMusicAlbums()
	if err != nil {
		return nil, err
	}
	for _, album := range game.MusicAlbums() {
		name := strings.TrimSpace(albums[album.Album])
		if name == "" {
			name = fmt.Sprintf("Album %d", album.Album)
		}
		reply.Music.Albums = append(reply.Music.Albums, archiveOption{ID: album.ID, Name: name})
	}

	ships, err := server.Store.ListShipsByCommander(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, ship := range ships {
		if ship.SkinID != 0 {
			reply.Skins[ship.ID] = archiveSkin{Skin: ship.SkinID, Name: strings.TrimSpace(server.names[dropSkin][ship.SkinID].Name)}
		}
	}
	return reply, nil
}

// loadMusicAlbums reads music_album's names (AppreciateProxy:
// getMainPlayerAlbumName), once.
var loadMusicAlbums = sync.OnceValues(func() (map[uint32]string, error) {
	rows, err := gamedata.Table[struct {
		Name string `json:"album_name"`
	}]("music_album")
	if err != nil {
		return nil, err
	}
	names := make(map[uint32]string, len(rows))
	for id, row := range rows {
		names[id] = row.Name
	}
	return names, nil
})

// handleArchiveMemories lists a memory group's memories.
func (server *Server) handleArchiveMemories(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	group, err := pathUint(r, "group")
	if err != nil {
		return nil, err
	}
	groups, err := server.Game.AdminMemoryGroups(r.Context(), commander.ID, group)
	if errors.Is(err, game.ErrNoStoryIndex) {
		return nil, badRequest(err.Error())
	} else if err != nil {
		return nil, err
	}
	for _, row := range groups {
		if row.ID == group {
			return row.Memories, nil
		}
	}
	return nil, httpError{http.StatusNotFound, fmt.Sprintf("no memory group %d", group)}
}

// handleArchiveEntry sets an archive entry: {level, intimacy, star,
// married, liked, retrofit}.
func (server *Server) handleArchiveEntry(r *http.Request) (any, error) {
	group, err := pathUint(r, "group")
	if err != nil {
		return nil, err
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, edit game.ArchiveEdit) (bool, error) {
		return server.Game.SetArchiveEntry(ctx, id, group, edit)
	})
}

// handleArchiveCollection marks a milestone award claimed (and those
// before it) or unclaimed (and those after it): {claimed}.
func (server *Server) handleArchiveCollection(r *http.Request) (any, error) {
	storeup, err := pathUint(r, "storeup")
	if err != nil {
		return nil, err
	}
	index, err := pathUint(r, "index")
	if err != nil {
		return nil, err
	}
	type body struct {
		Claimed bool `json:"claimed"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetCollectionClaimed(ctx, id, storeup, index, b.Claimed)
	})
}

// handleArchiveMedalTier marks a medal tier claimed (and those before it)
// or unclaimed (and those after it): {claimed}.
func (server *Server) handleArchiveMedalTier(r *http.Request) (any, error) {
	tier, err := pathUint(r, "tier")
	if err != nil {
		return nil, err
	}
	type body struct {
		Claimed bool `json:"claimed"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetMedalClaimed(ctx, id, tier, b.Claimed)
	})
}

// handleArchiveMedalDisplay sets the medals shown: {ids}.
func (server *Server) handleArchiveMedalDisplay(r *http.Request) (any, error) {
	type body struct {
		IDs []uint32 `json:"ids"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetDisplayMedals(ctx, id, b.IDs)
	})
}

// handleArchiveMemoriesSeen marks a group's memories seen or not: {memory
// (0 every one), seen}.
func (server *Server) handleArchiveMemoriesSeen(r *http.Request) (any, error) {
	group, err := pathUint(r, "group")
	if err != nil {
		return nil, err
	}
	type body struct {
		Memory uint32 `json:"memory"`
		Seen   bool   `json:"seen"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetMemoriesSeen(ctx, id, group, b.Memory, b.Seen)
	})
}

// handleArchiveCryptolalia lists the Cryptolalia stories, unlocked or not.
func (server *Server) handleArchiveCryptolalia(r *http.Request) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	return server.Game.AdminCryptolaliaList(r.Context(), commander.ID)
}

// handleArchiveCryptolaliaStory unlocks a Cryptolalia story for free or
// locks it again: {unlocked}.
func (server *Server) handleArchiveCryptolaliaStory(r *http.Request) (any, error) {
	story, err := pathUint(r, "story")
	if err != nil {
		return nil, err
	}
	type body struct {
		Unlocked bool `json:"unlocked"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		if story == 0 {
			return false, badRequest("no story 0")
		}
		return server.Game.SetCryptolaliaUnlocked(ctx, id, story, b.Unlocked)
	})
}

// handleArchiveMusic sets the main scene's music player: {album, mode}.
func (server *Server) handleArchiveMusic(r *http.Request) (any, error) {
	type body struct {
		Album uint32 `json:"album"`
		Mode  uint32 `json:"mode"`
	}
	return commanderEdit(server, r, func(ctx context.Context, id uint32, b body) (bool, error) {
		return server.Game.SetMusicPlayer(ctx, id, b.Album, b.Mode)
	})
}
