// Package gamedata reads the client's config tables at runtime, never embedded
// in the binary: <data dir>/<region>/{sharecfgdata,ShareCfg}/<table>.json,
// either an object keyed by id (the AzurLaneData layout, which the repo's
// data/ uses) or a list of rows with their id (the old belfast-data layout).
// The data dir is ./data, the one committed with the code, unless
// $BELFAST_DATA says otherwise. Content the official server sends that no
// table has lives next to the tables, in <data dir>/<region>/server.
package gamedata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"github.com/ggmolly/belfast/internal/region"
)

// Dir is the data dir.
func Dir() string {
	if dir := os.Getenv("BELFAST_DATA"); dir != "" {
		return dir
	}
	return "data"
}

// RegionDir is the current region's part of the data dir.
func RegionDir() string {
	return filepath.Join(Dir(), region.Current())
}

// errAbsent is wrapped by the errors for a table or gameset setting the
// region's data doesn't have at all: a feature its client hasn't received
// yet (KR and TW lag behind EN), see Feature. One that is there but
// unreadable is another error.
var errAbsent = errors.New("absent")

// Missing is the error for a table lacking a row or value the server needs.
func Missing(table, format string, args ...any) error {
	return fmt.Errorf("config table %s in %s: no %s", table, RegionDir(), fmt.Sprintf(format, args...))
}

// ServerFile reads server content (<region>/server/<name>), nil when the data
// dir has none.
func ServerFile(name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(RegionDir(), "server", name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

// file reads a table's file, an error when it is missing (errAbsent when the
// region has no such file at all) or has no rows.
func file(name string) ([]byte, error) {
	var raw []byte
	var err error
	found := false
	for _, sub := range []string{"sharecfgdata", "ShareCfg"} {
		raw, err = os.ReadFile(filepath.Join(RegionDir(), sub, name+".json"))
		found = found || !errors.Is(err, fs.ErrNotExist)
		if err == nil && len(bytes.TrimSpace(raw)) > 2 {
			break
		}
	}
	switch {
	case !found:
		err = errAbsent
	case err == nil && len(bytes.TrimSpace(raw)) <= 2, errors.Is(err, fs.ErrNotExist):
		err = errors.New("no rows")
	}
	if err != nil {
		return nil, fmt.Errorf("config table %s in %s: %w", name, RegionDir(), err)
	}
	return bytes.TrimSpace(raw), nil
}

// Table decodes every row of a table into T, keyed by id. Rows that don't fit
// T are skipped; a missing or empty table is an error.
func Table[T any](name string) (map[uint32]T, error) {
	raw, err := file(name)
	if err != nil {
		return nil, err
	}
	// the whole table at once; row by row only when a row doesn't fit T
	if raw[0] == '[' {
		var rows []T
		var ids []struct {
			ID uint32 `json:"id"`
		}
		if json.Unmarshal(raw, &rows) == nil && json.Unmarshal(raw, &ids) == nil {
			table := make(map[uint32]T, len(rows))
			for i, row := range rows {
				table[ids[i].ID] = row
			}
			return table, nil
		}
	} else {
		var table map[uint32]T
		if json.Unmarshal(raw, &table) == nil {
			return table, nil
		}
	}
	table, err := rows[T](raw)
	if err != nil {
		return nil, fmt.Errorf("config table %s: %w", name, err)
	}
	return table, nil
}

// rows decodes a table row by row, skipping the rows that don't fit T (and,
// in the object layout, the keys that aren't ids).
func rows[T any](raw []byte) (map[uint32]T, error) {
	table := map[uint32]T{}
	add := func(id uint32, row json.RawMessage) {
		var value T
		if json.Unmarshal(row, &value) == nil {
			table[id] = value
		}
	}
	if raw[0] == '[' {
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		for _, row := range list {
			var key struct {
				ID uint32 `json:"id"`
			}
			if json.Unmarshal(row, &key) == nil {
				add(key.ID, row)
			}
		}
		return table, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	for key, row := range object {
		if id, err := strconv.ParseUint(key, 10, 32); err == nil {
			add(uint32(id), row)
		}
	}
	return table, nil
}

// Gameset reads the gameset settings' key_value, by setting name.
func Gameset() (map[string]uint32, error) {
	raw, err := file("gameset")
	if err != nil {
		return nil, err
	}
	var rows map[string]struct {
		KeyValue uint32 `json:"key_value"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("config table gameset: %w", err)
	}
	gameset := make(map[string]uint32, len(rows))
	for key, row := range rows {
		gameset[key] = row.KeyValue
	}
	return gameset, nil
}

// Setting is a row of a settings table keyed by name (guildset...): a
// key_value, or key_args for the settings that hold a list; dorm3d_set
// writes its values as key_value_int, island_set as key_value_int and
// key_value_varchar (a list, or "" when unused).
type Setting struct {
	KeyValue        uint32          `json:"key_value"`
	KeyArgs         json.RawMessage `json:"key_args"`
	KeyValueInt     uint32          `json:"key_value_int"`
	KeyValueVarchar json.RawMessage `json:"key_value_varchar"`
}

// Settings reads a settings table, by setting name.
func Settings(name string) (map[string]Setting, error) {
	raw, err := file(name)
	if err != nil {
		return nil, err
	}
	var rows map[string]Setting
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("config table %s: %w", name, err)
	}
	return rows, nil
}

// Gametip reads the gametip strings (what the client's i18n(key) shows), by
// key.
func Gametip() (map[string]string, error) {
	raw, err := file("gametip")
	if err != nil {
		return nil, err
	}
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("config table gametip: %w", err)
	}
	tips := make(map[string]string, len(rows))
	for key, raw := range rows {
		var row struct {
			Tip string `json:"tip"`
		}
		if json.Unmarshal(raw, &row) == nil {
			tips[key] = row.Tip
		}
	}
	return tips, nil
}

// GamesetDescription decodes a gameset setting's description (the lists some
// settings hold instead of a key_value) into value.
func GamesetDescription(key string, value any) error {
	raw, err := file("gameset")
	if err != nil {
		return err
	}
	var rows map[string]struct {
		Description json.RawMessage `json:"description"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return fmt.Errorf("config table gameset: %w", err)
	}
	row, ok := rows[key]
	if !ok {
		return fmt.Errorf("config table gameset in %s: no %s: %w", RegionDir(), key, errAbsent)
	}
	if json.Unmarshal(row.Description, value) != nil {
		return Missing("gameset", "%s description", key)
	}
	return nil
}

// Load reads a table into *table as is.
func Load[T any](name string, table *map[uint32]T) func() error {
	return func() (err error) {
		*table, err = Table[T](name)
		return err
	}
}

// Feature runs the loads of a feature some regions' clients don't have yet,
// one table or setting each: off (false, nil) when the region lacks every
// one of them, else on with LoadAll's error, so a feature partly there
// fails.
func Feature(loads ...func() error) (bool, error) {
	absent := make([]bool, len(loads))
	wrapped := make([]func() error, len(loads))
	for i, load := range loads {
		wrapped[i] = func() error {
			err := load()
			absent[i] = errors.Is(err, errAbsent)
			return err
		}
	}
	err := LoadAll(wrapped...)
	if !slices.Contains(absent, false) {
		return false, nil
	}
	return true, err
}

// LoadAll runs independent loads concurrently.
func LoadAll(loads ...func() error) error {
	errs := make([]error, len(loads))
	var wg sync.WaitGroup
	for i, load := range loads {
		wg.Go(func() { errs[i] = load() })
	}
	wg.Wait()
	return errors.Join(errs...)
}
