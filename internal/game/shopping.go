package game

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/ggmolly/belfast/internal/connection"
	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/gamedata"
	"github.com/ggmolly/belfast/internal/logger"
	"github.com/ggmolly/belfast/internal/protobuf"
)

// shop_template genres (ShopArgs, model/const/shopargs.lua) belfast sells.
// The others need state belfast doesn't keep yet (the cruise pass skins,
// Operation Siren, dorm 3D...) and are refused.
const (
	genreNone           = ""
	genreGemShop        = "gem_shop"
	genreShoppingStreet = "shopping_street"
	genreArenaShop      = "arena_shop"
	genreBuyOil         = "buy_oil"
	genreGiftPackage    = "gift_package"
	genreNewServerShop  = "new_server_shop"
	// the backyard's Expansion License and food capacity
	genreBackyardUpgrade    = "backyard_upgrade"
	genreBackyardFoodExtend = "backyard_food_extend"
	// a Tactical Class room
	genreSkillRoomUpgrade = "skill_room_upgrade"
	// bought through SkinShoppingCommand
	genreSkinShop          = "skin_shop"
	genreSkinShopTimeLimit = "skin_shop_timelimit"
	// the Private Quarters shop (Dorm3dShopUI): its gifts (type 27) and
	// furniture (type 26) both have this genre
	genreDorm3dGift = "dorm3d_gift"
)

var sellableGenres = []string{genreNone, genreGemShop, genreShoppingStreet, genreArenaShop, genreBuyOil, genreGiftPackage, genreNewServerShop, genreSkinShop, genreSkinShopTimeLimit, genreBackyardUpgrade, genreBackyardFoodExtend, genreSkillRoomUpgrade, genreDorm3dGift}

// shopTypeSkin is shop_template type of the skin goods: effect_args[1] is a
// ship_skin_template id.
const shopTypeSkin = 6

// the type 0 goods' effect_args belfast stores (ExtendCommand,
// controller/command/shop/extendcommand.lua)
const (
	effectShipBagSize      = "ship_bag_size"
	effectEquipBagSize     = "equip_bag_size"
	effectCommanderBagSize = "commander_bag_size"
	effectDormExpPos       = "dorm_exp_pos"
	effectDormFoodMax      = "dorm_food_max"
	effectDormFloor        = "dorm_floor"
	effectSkillRoomPos     = "skill_room_pos"
)

// Player.MAX_SHIP_BAG, MAX_EQUIP_BAG and MAX_COMMANDER_BAG (model/vo/player.lua)
const (
	maxShipBag      = 4000
	maxEquipBag     = 2000
	maxCommanderBag = 400
)

// shop_template group_type: when a group's purchases reset. The Lua never
// reads it; it is 1 on the daily supplies pack, 2 on the weekly free pack
// and 3 on the monthly packs.
const (
	groupDaily   = 1
	groupWeekly  = 2
	groupMonthly = 3
)

// buyOilLimit is gameset buy_oil_limit, the oil purchases a day.
var buyOilLimit uint32

// shop_template row
type shopConfig struct {
	Genre         string          `json:"genre"`
	Type          uint32          `json:"type"`
	EffectArgs    json.RawMessage `json:"effect_args"`
	Num           int64           `json:"num"`
	ResourceType  uint32          `json:"resource_type"`
	ResourceNum   int64           `json:"resource_num"`
	Discount      uint32          `json:"discount"`
	DiscountTime  json.RawMessage `json:"discount_time"`
	LimitArgs     json.RawMessage `json:"limit_args"`
	Time          gamedata.Window `json:"time"`
	Group         uint32          `json:"group"`
	GroupType     uint32          `json:"group_type"`
	GroupLimit    uint32          `json:"group_limit"`
	GroupBuyCount uint32          `json:"group_buy_count"`
	// skin_shop_timelimit: the seconds one purchase lasts
	TimeSecond int64 `json:"time_second"`
}

// shopGoods is a shop_template row with its value formats read.
type shopGoods struct {
	shopConfig
	effect string // type 0: what it extends (effect_args)
	target uint32 // other types: the drop id (effect_args[1])
	// limit_args: the commander level needed, the purchase limit ("time"),
	// the range a counter must be in ("count": the oil bought today, a
	// capacity before its extension)
	level, times       uint32
	hasCount           bool
	countMin, countMax uint32
	// discount_time: "always" and {} are always (CommonCommodity.InCommodityDiscountTime)
	discountTime gamedata.Window
}

var shopTemplate map[uint32]shopGoods

func loadShopData() error {
	var rows map[uint32]shopConfig
	var gameset map[string]uint32
	err := gamedata.LoadAll(
		gamedata.Load("shop_template", &rows),
		func() (err error) {
			gameset, err = gamedata.Gameset()
			return err
		},
	)
	if err != nil {
		return err
	}
	limit, ok := gameset["buy_oil_limit"]
	if !ok {
		return gamedata.Missing("gameset", "buy_oil_limit")
	}
	buyOilLimit = limit
	shopTemplate = make(map[uint32]shopGoods, len(rows))
	for id, row := range rows {
		goods := shopGoods{shopConfig: row}
		if json.Unmarshal(row.EffectArgs, &goods.effect) != nil {
			if ids := gamedata.IDs(row.EffectArgs); len(ids) > 0 {
				goods.target = ids[0]
			}
		}
		goods.readLimits()
		var always string
		var window []json.RawMessage
		switch {
		case json.Unmarshal(row.DiscountTime, &always) == nil:
			goods.discountTime.Always = always == "always"
		case json.Unmarshal(row.DiscountTime, &window) == nil && len(window) == 0:
			goods.discountTime.Always = true
		default:
			_ = json.Unmarshal(row.DiscountTime, &goods.discountTime)
		}
		shopTemplate[id] = goods
	}
	return nil
}

// readLimits reads limit_args: one ["name", args...] or a list of them.
func (goods *shopGoods) readLimits() {
	var list []json.RawMessage
	if json.Unmarshal(goods.LimitArgs, &list) != nil || len(list) == 0 {
		return
	}
	var name string
	if json.Unmarshal(list[0], &name) == nil {
		list = []json.RawMessage{goods.LimitArgs}
	}
	for _, raw := range list {
		var entry []json.RawMessage
		if json.Unmarshal(raw, &entry) != nil || len(entry) < 2 || json.Unmarshal(entry[0], &name) != nil {
			continue
		}
		args := make([]uint32, len(entry)-1)
		for i := range args {
			_ = json.Unmarshal(entry[i+1], &args[i])
		}
		switch {
		case name == "level":
			goods.level = args[0]
		case name == "time":
			goods.times = args[0]
		case (name == "count" || name == "number") && len(args) >= 2:
			goods.hasCount, goods.countMin, goods.countMax = true, args[0], args[1]
		}
	}
}

// countAllows is whether a counter is in the limit_args count range.
func (goods shopGoods) countAllows(n uint32) bool {
	return !goods.hasCount || (goods.countMin <= n && n <= goods.countMax)
}

// resetAt is when the commander's purchases of goods stop counting (0
// never): the general shop's stock at its refresh, the merit shop's and the
// oil bought at midnight.
func (goods shopGoods) resetAt(now time.Time) int64 {
	switch goods.Genre {
	case genreShoppingStreet:
		return nextExerciseReset(now).Unix()
	case genreArenaShop, genreBuyOil:
		return nextDay(now).Unix()
	}
	return 0
}

// groupResetAt is when the purchases of goods' group stop counting (0
// never).
func (goods shopGoods) groupResetAt(now time.Time) int64 {
	switch goods.GroupType {
	case groupDaily:
		return nextDay(now).Unix()
	case groupWeekly:
		return periodStart(now, week).Add(week).Unix()
	case groupMonthly:
		return nextMonth(now).Unix()
	}
	return 0
}

// price is what count goods cost (ShoppingCommand): resource_num each, the
// general shop's discount rounded up, then the row's own discount while it
// runs. false for the goods priced from the academy levels (resource_num -1).
func (goods shopGoods) price(count, streetDiscount uint32, now time.Time) (uint32, bool) {
	if goods.ResourceNum < 0 {
		return 0, false
	}
	price := uint64(goods.ResourceNum) * uint64(count)
	if goods.Genre == genreShoppingStreet {
		price = (price*uint64(streetDiscount) + 99) / 100
	}
	if goods.Discount != 0 && goods.Discount <= 100 && goods.discountTime.Open(now) {
		price = price * uint64(100-goods.Discount) / 100
	}
	return uint32(price), true
}

// amount is what count goods give: num each, the oil bought by level for
// the oil purchases (num -1, ShopArgs.getOilByLevel: 500 + 3 per level).
func (goods shopGoods) amount(count, level uint32) (uint32, bool) {
	switch {
	case goods.Num == -1 && goods.Genre == genreBuyOil:
		return (500 + 3*level) * count, true
	case goods.Num < 0:
		return 0, false
	}
	return uint32(goods.Num) * count, true
}

// fieldUpgradePrice is what the academy field upgrade goods cost
// (ShoppingCommand, resource_num -1): use[2] of the field's level row, in
// the goods' resource_type. false when the upgrade can't start
// (BaseResourceField:CanUpgrade): one at a time (the client sends count
// 1), below its row's user_level, at the max level, while one runs, or
// the lecture hall before it opens (the client can't load its scene,
// SystemOpenMgr).
func (goods shopGoods) fieldUpgradePrice(commander db.Commander, count uint32) (uint32, bool) {
	table := fieldUpgrades[goods.effect]
	level, upgradeAt := fieldLevel(commander, goods.effect)
	upgrade, ok := table.rows[level]
	if !ok || count != 1 || upgradeAt != 0 || level >= table.max || commander.Level < upgrade.UserLevel ||
		(goods.effect == effectClassFieldLevel && commander.Level < classOpenLevel) {
		return 0, false
	}
	return upgrade.Use[1], true
}

// shopPurchases is how many times the commander bought each shop_template
// row since it last reset.
func shopPurchases(ctx context.Context, queries *db.Queries, commanderID uint32, now time.Time) (map[uint32]uint32, error) {
	rows, err := queries.ListShopPurchases(ctx, db.ListShopPurchasesParams{CommanderID: commanderID, Now: now.Unix()})
	if err != nil {
		return nil, err
	}
	purchases := make(map[uint32]uint32, len(rows))
	for _, row := range rows {
		purchases[row.ShopID] += row.Count
	}
	return purchases, nil
}

// shopGroupPurchases is what the commander bought of each shop_template
// group since it last reset.
func shopGroupPurchases(ctx context.Context, queries *db.Queries, commanderID uint32, now time.Time) (map[uint32]uint32, error) {
	rows, err := queries.ListShopGroupPurchases(ctx, db.ListShopGroupPurchasesParams{CommanderID: commanderID, Now: now.Unix()})
	if err != nil {
		return nil, err
	}
	purchases := make(map[uint32]uint32, len(rows))
	for _, row := range rows {
		purchases[row.GroupID] += row.Count
	}
	return purchases, nil
}

// oilBought is how many times the commander bought oil today
// (SC_11003.buy_oil_count), from its shopPurchases.
func oilBought(purchases map[uint32]uint32) uint32 {
	total := uint32(0)
	for id, count := range purchases {
		if shopTemplate[id].Genre == genreBuyOil {
			total += count
		}
	}
	return total
}

// streetDiscount is the discount the general shop sells a row at, false
// when it isn't in stock.
func streetDiscount(id uint32) (uint32, bool) {
	for _, goods := range shopStreetGoods {
		if goods[0] == id {
			return goods[1], true
		}
	}
	return 0, false
}

// handleShopping answers ShoppingCommand and SkinShoppingCommand
// (controller/command/shop/), a shop_template purchase of number goods. On
// result 0 the client takes the price off its own resources, counts the
// purchase against the goods' shop (general shop stock, merit shop, oil
// bought today, gift package limits and groups), applies the type 0
// extensions itself (ExtendCommand) and adds drop_list. No official capture
// has it: the reply is built from the Lua alone.
func (game *Game) handleShopping(client *connection.Client, req *protobuf.CS_16001) error {
	ctx := context.Background()
	var drops []*protobuf.DROPINFO
	err := game.tx(ctx, client, func(tx *Tx) (err error) {
		drops, err = tx.buyGoods(req.GetId(), req.GetNumber(), time.Now())
		return err
	})
	if err != nil && !refused(err) {
		return err
	}
	if err != nil {
		logger.WithFields("Shop", logger.FieldValue("id", req.GetId()), logger.FieldValue("number", req.GetNumber())).Warn("purchase refused")
		drops = nil
	}
	return client.Send(&protobuf.SC_16002{Result: result(err), DropList: drops})
}

// buyGoods buys count of a shop_template row: checks it can be bought, spends
// its price, gives it and counts the purchase. It returns the drops given.
func (tx *Tx) buyGoods(id, count uint32, now time.Time) ([]*protobuf.DROPINFO, error) {
	goods, ok := shopTemplate[id]
	if !ok || count == 0 || !slices.Contains(sellableGenres, goods.Genre) || !goods.Time.Open(now) {
		return nil, errRefused
	}
	commander, err := tx.GetCommander(tx.ctx, tx.commanderID)
	if err != nil {
		return nil, err
	}
	purchases, err := shopPurchases(tx.ctx, tx.Queries, tx.commanderID, now)
	if err != nil {
		return nil, err
	}
	groups, err := shopGroupPurchases(tx.ctx, tx.Queries, tx.commanderID, now)
	if err != nil {
		return nil, err
	}
	if commander.Level < goods.level || (goods.times > 0 && purchases[id] >= goods.times) ||
		(goods.Group > 0 && goods.GroupLimit > 0 && groups[goods.Group] >= goods.GroupLimit) {
		return nil, errRefused
	}
	discount := uint32(100)
	switch goods.Genre {
	case genreShoppingStreet:
		// one of each in stock (SC_22102)
		if discount, ok = streetDiscount(id); !ok || purchases[id] > 0 {
			return nil, errRefused
		}
	case genreArenaShop:
		// today's goods, once each (Goods.TYPE_MILITARY)
		goods, _, err := meritShopGoods(tx.ctx, tx.Queries, tx.commanderID, now)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(goods, id) || purchases[id] > 0 {
			return nil, errRefused
		}
	case genreBuyOil:
		// the row priced for the oil bought so far today (ShoppingStreet.getRiseShopId)
		if bought := oilBought(purchases); bought >= buyOilLimit || !goods.countAllows(bought) {
			return nil, errRefused
		}
	case genreDorm3dGift:
		// limit_args {"count", 0, max}: bought at most max times for good
		// (ShoppingCommand counts it in ApartmentProxy's permanent shop
		// counts, SC_28000 gift_permanent / furniture_permanent)
		if goods.hasCount && purchases[id]+count > goods.countMax {
			return nil, errRefused
		}
	case genreSkinShop, genreSkinShopTimeLimit:
		if err := tx.canBuySkin(goods, count, now); err != nil {
			return nil, err
		}
	}
	price, ok := goods.price(count, discount, now)
	if _, field := fieldUpgrades[goods.effect]; field && goods.Type == 0 {
		// an upgrade that ended raises the level this one starts from
		if err := tx.settleResourceFields(commander, now); err != nil {
			return nil, err
		}
		if commander, err = tx.GetCommander(tx.ctx, tx.commanderID); err != nil {
			return nil, err
		}
		price, ok = goods.fieldUpgradePrice(commander, count)
	}
	amount, okAmount := goods.amount(count, commander.Level)
	if !ok || !okAmount {
		return nil, errRefused
	}
	if err := tx.SpendShopResource(goods.ResourceType, price); err != nil {
		return nil, err
	}
	var drops []*protobuf.DROPINFO
	switch goods.Type {
	case 0:
		err = tx.extend(goods, commander, amount, now)
	case dropTypeResource, dropTypeItem, dropTypeShip, dropTypeVItem:
		drops = []*protobuf.DROPINFO{dropInfo(goods.Type, goods.target, amount)}
		err = tx.grantDrops(drops)
	case dropTypeDorm3dFurniture, dropTypeDorm3dGift:
		// the client adds drop_list to ApartmentProxy (Drop: changeGiftCount,
		// AddFurnitureByID)
		drops = []*protobuf.DROPINFO{dropInfo(goods.Type, goods.target, amount)}
		err = tx.addDorm3dDrop(goods.Type, goods.target, amount)
	case shopTypeSkin:
		// SkinShoppingCommand adds the skin itself and drop_list besides:
		// a DROP_TYPE_SKIN there would make a time-limited one permanent,
		// so none is sent
		err = tx.buySkin(goods, count, now)
	default:
		err = errRefused
	}
	if err != nil {
		return nil, err
	}
	// the client counts one purchase whatever the number (ShoppingCommand)
	if err := tx.AddShopPurchase(tx.ctx, db.AddShopPurchaseParams{CommanderID: tx.commanderID, ShopID: id, ExpiresAt: goods.resetAt(now), Count: 1}); err != nil {
		return nil, err
	}
	if goods.Group > 0 {
		err = tx.AddShopGroupPurchase(tx.ctx, db.AddShopGroupPurchaseParams{CommanderID: tx.commanderID, GroupID: goods.Group, ExpiresAt: goods.groupResetAt(now), Count: goods.GroupBuyCount})
	}
	return drops, err
}

// extend applies a type 0 purchase, a capacity extension of amount (the
// count limit is the capacity it extends from) or an academy field
// upgrade.
func (tx *Tx) extend(goods shopGoods, commander db.Commander, amount uint32, now time.Time) error {
	if ok, err := tx.extendDorm(goods, amount); ok {
		return err
	}
	switch goods.effect {
	case effectOilFieldLevel, effectGoldFieldLevel, effectClassFieldLevel:
		// ExtendCommand starts the field's timer: its level's time
		// (NavalAcademyProxy:StartUpGradeSuccess); fieldUpgradePrice
		// checked it can start
		level, _ := fieldLevel(commander, goods.effect)
		upgrade := fieldUpgrades[goods.effect].rows[level]
		return tx.setFieldLevel(goods.effect, level, now.Unix()+upgrade.Time)
	case effectShipBagSize:
		if !goods.countAllows(commander.ShipBagMax) || commander.ShipBagMax+amount > maxShipBag {
			return errRefused
		}
		return tx.AddShipBagMax(tx.ctx, db.AddShipBagMaxParams{ShipBagMax: amount, ID: commander.ID})
	case effectEquipBagSize:
		if !goods.countAllows(commander.EquipBagMax) || commander.EquipBagMax+amount > maxEquipBag {
			return errRefused
		}
		return tx.AddEquipBagMax(tx.ctx, db.AddEquipBagMaxParams{EquipBagMax: amount, ID: commander.ID})
	case effectCommanderBagSize:
		if !goods.countAllows(commander.CommanderBagMax) || commander.CommanderBagMax+amount > maxCommanderBag {
			return errRefused
		}
		return tx.AddCommanderBagMax(tx.ctx, db.AddCommanderBagMaxParams{CommanderBagMax: amount, ID: commander.ID})
	case effectSkillRoomPos:
		// limit_args "number": the rooms open before
		if !goods.countAllows(commander.SkillClassNum) || commander.SkillClassNum >= maxSkillClassNum {
			return errRefused
		}
		return tx.AddSkillClassNum(tx.ctx, commander.ID)
	}
	return errRefused
}
