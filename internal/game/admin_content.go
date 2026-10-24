package game

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
)

// What the back-office's content screens read and change: mail sent to many
// commanders, the redeem codes of server/exchange_codes.json, the activity
// bosses' marks, the surveys taken and the server's schedule.

// Broadcast sends mail to each commander of ids in one transaction, as a
// compensation expiring at expires when it isn't zero, and tells the ones
// playing their mailbox (or compensations) changed. It returns how many were
// sent.
func (game *Game) Broadcast(ctx context.Context, ids []uint32, mail Mail, expires time.Time) (int, error) {
	if _, err := mail.check(); err != nil {
		return 0, err
	}
	now := time.Now()
	if !expires.IsZero() && !expires.After(now) {
		return 0, fmt.Errorf("compensation expiring before it is sent")
	}
	err := game.DB.InTx(ctx, func(queries *db.Queries) error {
		for _, id := range ids {
			var err error
			if expires.IsZero() {
				_, err = SendMail(ctx, queries, id, mail, now)
			} else {
				_, err = SendCompensation(ctx, queries, id, mail, now, expires)
			}
			if err != nil {
				return fmt.Errorf("commander %d: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	notify := game.NotifyMail
	if !expires.IsZero() {
		notify = game.NotifyCompensations
	}
	for _, id := range ids {
		if err := notify(ctx, id); err != nil {
			return len(ids), err
		}
	}
	return len(ids), nil
}

// ExchangeCode is a redeem code of server/exchange_codes.json as the
// back-office shows it: Always without a window, else open from Start to End
// (server clock).
type ExchangeCode struct {
	Code, Title, Sender, Content string
	Rewards                      [][3]uint32
	Always                       bool
	Start, End                   time.Time
}

// ExchangeCodes lists the redeem codes the server accepts, by code.
func ExchangeCodes() []ExchangeCode {
	exchangeCodesMu.RLock()
	defer exchangeCodesMu.RUnlock()
	list := make([]ExchangeCode, 0, len(exchangeCodes))
	for key, code := range exchangeCodes {
		row := ExchangeCode{Code: key, Title: code.Title, Sender: code.Sender, Content: code.Content, Rewards: code.Rewards, Always: code.Window == nil}
		if code.Window != nil {
			row.Always = code.Window.Always
			row.Start, row.End = code.Window.Start, code.Window.End
		}
		list = append(list, row)
	}
	slices.SortFunc(list, func(a, b ExchangeCode) int { return cmp.Compare(a.Code, b.Code) })
	return list
}

// ErrNoCode is the error for a code exchange_codes.json doesn't have.
var ErrNoCode = errors.New("no such code")

// CreateExchangeCode adds a code to exchange_codes.json and reloads the
// codes. A code spelled like an existing one (without regard to case) or
// whose mail couldn't be stored is refused.
func CreateExchangeCode(code ExchangeCode) error {
	key := strings.ToUpper(strings.TrimSpace(code.Code))
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return fmt.Errorf("code %q: want one word", code.Code)
	}
	if len(code.Rewards) == 0 {
		return fmt.Errorf("a code without rewards does nothing")
	}
	mail := exchangeCode{Title: cmp.Or(code.Title, key), Sender: code.Sender, Content: code.Content, Rewards: code.Rewards}.mail()
	if _, err := mail.check(); err != nil {
		return err
	}
	entry := map[string]any{"rewards": code.Rewards}
	for field, value := range map[string]string{"title": code.Title, "sender": code.Sender, "content": code.Content} {
		if value != "" {
			entry[field] = value
		}
	}
	if !code.Always {
		if code.Start.IsZero() || !code.End.After(code.Start) {
			return fmt.Errorf("the code must end after it starts")
		}
		entry["time"] = timerWindow(code.Start, code.End)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return editExchangeCodes(func(file map[string]json.RawMessage) error {
		if name := fileKey(file, key); name != "" {
			return fmt.Errorf("code %s already exists", name)
		}
		file[key] = raw
		return nil
	})
}

// EndExchangeCode closes a code's window at now: redeeming it then answers
// that it expired. A code without a window opens at now too.
func EndExchangeCode(code string, now time.Time) error {
	return editExchangeCodes(func(file map[string]json.RawMessage) error {
		name := fileKey(file, strings.ToUpper(strings.TrimSpace(code)))
		if name == "" {
			return ErrNoCode
		}
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(file[name], &entry); err != nil {
			return fmt.Errorf("exchange_codes.json %s: %w", name, err)
		}
		start := now
		if raw, ok := entry["time"]; ok {
			var window gamedata.Window
			if json.Unmarshal(raw, &window) == nil && !window.Start.IsZero() && window.Start.Before(now) {
				start = window.Start
			}
		}
		end := now.Truncate(time.Second)
		window, err := json.Marshal(timerWindow(start, end))
		if err != nil {
			return err
		}
		entry["time"] = window
		raw, err := json.Marshal(entry)
		file[name] = raw
		return err
	})
}

// DeleteExchangeCode removes a code from exchange_codes.json; its
// redemptions stay.
func DeleteExchangeCode(code string) error {
	return editExchangeCodes(func(file map[string]json.RawMessage) error {
		name := fileKey(file, strings.ToUpper(strings.TrimSpace(code)))
		if name == "" {
			return ErrNoCode
		}
		delete(file, name)
		return nil
	})
}

// fileKey is how exchange_codes.json spells code (upper case), empty when it
// doesn't have it.
func fileKey(file map[string]json.RawMessage, code string) string {
	for name := range file {
		if strings.ToUpper(strings.TrimSpace(name)) == code {
			return name
		}
	}
	return ""
}

// timerWindow is a ["timer", start, end] window, server clock.
func timerWindow(start, end time.Time) []any {
	clock := func(at time.Time) [2][3]int {
		at = at.In(gamedata.Zone())
		return [2][3]int{{at.Year(), int(at.Month()), at.Day()}, {at.Hour(), at.Minute(), at.Second()}}
	}
	return []any{"timer", clock(start), clock(end)}
}

// editExchangeCodes changes exchange_codes.json (the codes by their
// spelling there, every other field kept as it is) and reloads the codes.
// The new file is checked as loadExchangeCodes reads it before it replaces
// the old one, and the codes swap under exchangeCodesMu, so a redemption
// sees either all of the old codes or all of the new ones.
func editExchangeCodes(edit func(map[string]json.RawMessage) error) error {
	exchangeCodesMu.Lock()
	defer exchangeCodesMu.Unlock()
	raw, err := gamedata.ServerFile("exchange_codes.json")
	if err != nil {
		return err
	}
	file := map[string]json.RawMessage{}
	if raw != nil {
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("exchange_codes.json: %w", err)
		}
	}
	if err := edit(file); err != nil {
		return err
	}
	// one code a line
	keys := slices.Sorted(maps.Keys(file))
	out := []byte("{\n")
	for i, key := range keys {
		name, err := json.Marshal(key)
		if err != nil {
			return err
		}
		var entry bytes.Buffer
		if err := json.Compact(&entry, file[key]); err != nil {
			return err
		}
		out = append(append(append(append(out, "  "...), name...), ": "...), entry.Bytes()...)
		if i < len(keys)-1 {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	out = append(out, "}\n"...)
	codes, err := parseExchangeCodes(out)
	if err != nil {
		return err
	}
	if err := writeServerFile("exchange_codes.json", out); err != nil {
		return err
	}
	exchangeCodes = codes
	return nil
}

// writeServerFile replaces <region>/server/<name> (where a link to it
// points) with data, through a temporary file renamed over it.
func writeServerFile(name string, data []byte) error {
	dir := filepath.Join(gamedata.RegionDir(), "server")
	path := filepath.Join(dir, name)
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		path = filepath.Join(resolved, name)
	} else {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		os.Chmod(temp.Name(), info.Mode().Perm())
	} else {
		os.Chmod(temp.Name(), 0o644)
	}
	return os.Rename(temp.Name(), path)
}

// ActivityBoss is a server-wide boss (see activitybosses.go) and the marks
// its HP reached: a boss battle's four bars, or a collab series' death.
type ActivityBoss struct {
	ActivityID, BossID uint32
	Collab             bool
	Series             uint32
	Name               string
	Open               bool
	// what is left of its HP, in 1/10000
	HP    uint32
	Marks []uint32 // the marks it has, highest first
	// when each reached mark was first seen
	Reached map[uint32]int64
}

// extraenemy_template and extraenemy_series_template names
type bossNames struct {
	Name         string `json:"name"`
	ChapterName2 string `json:"chapter_name2"`
}

// ActivityBosses are the bosses of the boss activities running at now or
// with marks stored, their HP counted as their pages count it (without
// stamping the marks).
func (game *Game) ActivityBosses(ctx context.Context, now time.Time) ([]ActivityBoss, error) {
	queries := game.DB.Queries
	marks, err := queries.ListAllActivityBossMarks(ctx)
	if err != nil {
		return nil, err
	}
	reached := map[[2]uint32]map[uint32]int64{}
	stored := map[uint32]bool{}
	for _, mark := range marks {
		key := [2]uint32{mark.ActivityID, mark.BossID}
		if reached[key] == nil {
			reached[key] = map[uint32]int64{}
		}
		reached[key][mark.Mark] = mark.ReachedAt
		stored[mark.ActivityID] = true
	}
	enemies, err := gamedata.Table[bossNames]("extraenemy_template")
	if err != nil {
		return nil, err
	}
	series, err := gamedata.Table[bossNames]("extraenemy_series_template")
	if err != nil {
		return nil, err
	}
	var bosses []ActivityBoss
	for _, config := range activityConfigs {
		open := config.Time.Open(now)
		if !open && !stored[config.ID] {
			continue
		}
		switch config.Type {
		case activityTypeBossBattleMark2:
			boss := activityWorldBossConfigs[config.ConfigID].BossID
			if len(boss) == 0 {
				continue
			}
			var damage uint32
			for _, id := range bossPtActivities(config.ID) {
				pts, err := queries.ListActivityData1(ctx, id)
				if err != nil {
					return nil, err
				}
				for _, pt := range pts {
					damage += pt
				}
			}
			bosses = append(bosses, ActivityBoss{
				ActivityID: config.ID, BossID: boss[0], Name: enemies[boss[0]].Name, Open: open,
				HP: bossHpRate(extraEnemyConfigs[boss[0]].HP, damage), Marks: activityBossMilestones,
				Reached: reachedOf(reached, config.ID, boss[0]),
			})
		case activityTypeBossRushDalCollab:
			lists, err := queries.ListActivityData1Lists(ctx, config.ID)
			if err != nil {
				return nil, err
			}
			wins := map[uint32]uint32{}
			for _, list := range lists {
				for _, id := range list {
					wins[id]++
				}
			}
			ids := gamedata.IDs(config.ConfigData)
			for i := len(ids) - 1; i >= 0; i-- {
				id := ids[i]
				boss := extraEnemySeriesConfigs[id].BossID
				if len(boss) == 0 {
					continue
				}
				bosses = append(bosses, ActivityBoss{
					ActivityID: config.ID, BossID: boss[0], Collab: true, Series: id,
					Name: cmp.Or(series[id].ChapterName2, enemies[boss[0]].Name), Open: open,
					HP: bossHpRate(extraEnemyConfigs[boss[0]].HP, wins[id]), Marks: []uint32{0},
					Reached: reachedOf(reached, config.ID, boss[0]),
				})
			}
		}
	}
	slices.SortStableFunc(bosses, func(a, b ActivityBoss) int { return cmp.Compare(b.ActivityID, a.ActivityID) })
	return bosses, nil
}

// reachedOf is the marks reached of a boss, never nil.
func reachedOf(reached map[[2]uint32]map[uint32]int64, activityID, bossID uint32) map[uint32]int64 {
	if marks := reached[[2]uint32{activityID, bossID}]; marks != nil {
		return marks
	}
	return map[uint32]int64{}
}

// ResetBossMark forgets when a boss's HP reached mark: the next time its
// page is opened the mark is stamped again if the HP is still at or below
// it. The client asks for the marks each time (CS_26031, CS_26081) and
// keeps none, so no one is kicked. false when there was no such mark.
func (game *Game) ResetBossMark(ctx context.Context, activityID, bossID, mark uint32) (bool, error) {
	deleted, err := game.DB.DeleteActivityBossMark(ctx, db.DeleteActivityBossMarkParams{ActivityID: activityID, BossID: bossID, Mark: mark})
	return deleted > 0, err
}

// Survey is a questionnaire (survey_data_template) as the back-office lists
// it: how many commanders took it and when the latest did.
type Survey struct {
	ID     uint32
	Name   string
	Open   bool
	Taken  int64
	Latest int64
}

// Surveys lists the surveys running at now and the ones taken, newest
// first.
func (game *Game) Surveys(ctx context.Context, now time.Time) ([]Survey, error) {
	taken, err := game.DB.ListSurveysTaken(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[uint32]*Survey{}
	for _, row := range taken {
		byID[row.SurveyID] = &Survey{ID: row.SurveyID, Name: surveys[row.SurveyID].Name, Taken: row.Taken, Latest: row.Latest}
	}
	for _, config := range openActivities(now) {
		if config.Type != activityTypeSurvey {
			continue
		}
		survey := byID[config.ConfigID]
		if survey == nil {
			survey = &Survey{ID: config.ConfigID, Name: surveys[config.ConfigID].Name}
			byID[config.ConfigID] = survey
		}
		survey.Open = true
	}
	list := make([]Survey, 0, len(byID))
	for _, survey := range byID {
		list = append(list, *survey)
	}
	slices.SortFunc(list, func(a, b Survey) int { return cmp.Compare(b.ID, a.ID) })
	return list, nil
}

// ResetSurvey forgets who took a survey, so each can take it (and get its
// bonus) again. The client keeps whether it took it (SC_11028), so the
// takers playing are kicked first. It returns how many took it.
func (game *Game) ResetSurvey(ctx context.Context, survey uint32) (int64, error) {
	takers, err := game.DB.ListSurveyTakers(ctx, survey)
	if err != nil {
		return 0, err
	}
	for _, id := range takers {
		if _, err := game.Kick(id); err != nil {
			return 0, err
		}
	}
	return game.DB.DeleteSurveysDone(ctx, survey)
}

// ShopRotation is when one of the shops belfast stocks gets new goods or
// forgets what was bought.
type ShopRotation struct {
	Name, Rule string
	Next       time.Time
}

// ShopRotations are the shops' next refreshes after now, as the handlers
// compute them.
func ShopRotations(now time.Time) []ShopRotation {
	nextWeek := periodStart(now, week).Add(week)
	return []ShopRotation{
		{"Daily goods", "Every day", nextDay(now)},
		{"Weekly goods", "Every Monday", nextWeek},
		{"Merit shop", "Every day", nextDay(now)},
		{"Guild shop", "Guild refresh days", nextGuildShopRefresh(now)},
		{"Medal shop", "On the 1st", nextMonth(now)},
		{"Core Data and Prototype", "On the 1st", nextMonth(now)},
		{"Mini-game shop", "On the 1st", nextMonth(now)},
	}
}

// BuildPool is a construction pool's rates by ship rarity (rate_tip, the
// rarities it has ships of), in percent.
type BuildPool struct {
	ID    uint32
	Name  string
	Rates map[uint32]float64
	Ships int
}

// BuildPools are the pools handleBuildShip draws from.
func BuildPools() []BuildPool {
	pools := make([]BuildPool, 0, len(buildPools))
	for id, pool := range buildPools {
		row := BuildPool{ID: id, Name: buildPoolNames[id], Rates: map[uint32]float64{}}
		for _, rate := range pool.rates {
			row.Rates[rate.rarity] = rate.rate
			row.Ships += len(pool.candidates[rate.rarity])
		}
		pools = append(pools, row)
	}
	slices.SortFunc(pools, func(a, b BuildPool) int { return cmp.Compare(a.ID, b.ID) })
	return pools
}

// ServerClock is the server's clock and resets at now.
type ServerClock struct {
	Now, NextDay, NextWeek, NextMonth time.Time
	Zone                              string
	Offset                            int // seconds east of UTC
}

// Clock is the server clock at now.
func Clock(now time.Time) ServerClock {
	local := now.In(gamedata.Zone())
	zone, offset := local.Zone()
	return ServerClock{Now: local, NextDay: nextDay(now), NextWeek: periodStart(now, week).Add(week), NextMonth: nextMonth(now), Zone: zone, Offset: offset}
}
