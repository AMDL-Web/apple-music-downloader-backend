package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Every config key can be overridden with an environment variable named
// AMDL_<SECTION>_<KEY> — the yaml path uppercased with "_" as the separator,
// for example AMDL_SERVER_LISTEN, AMDL_WRAPPER_ADDRESS, or
// AMDL_DOWNLOAD_QUALITY_PRIORITY. Overrides sit on top of the config file:
// they are applied on every Load (startup and Store.Reload alike) and never
// written back by the loader, so unsetting a variable restores the file
// value on the next start (a PUT /api/v1/config rewrite of the file does
// persist the effective values). Value syntax: strings verbatim, booleans per
// strconv.ParseBool, integers as digits, string lists as comma-separated
// items (an empty value is an empty list).

// envPrefix is the shared prefix of every backend environment variable.
const envPrefix = "AMDL_"

// envIgnored are AMDL_-prefixed variables that are not config-key overrides:
// the file-path variables read in main.
var envIgnored = map[string]struct{}{
	"AMDL_CONFIG":         {},
	"AMDL_RUNTIME_CONFIG": {},
	"AMDL_HOOKS_CONFIG":   {},
}

// envField ties one leaf field of Config to the environment variable that
// overrides it: the dotted config key, the variable name, and the field index
// path for reflect.Value.FieldByIndex.
type envField struct {
	key   string
	name  string
	index []int
}

// envFields enumerates every leaf field of Config from the yaml struct tags,
// so a field added to any section becomes overridable automatically.
func envFields() []envField {
	var fields []envField
	cfgType := reflect.TypeOf(Config{})
	for i := 0; i < cfgType.NumField(); i++ {
		section := cfgType.Field(i)
		sectionTag := yamlTagName(section)
		for j := 0; j < section.Type.NumField(); j++ {
			leafTag := yamlTagName(section.Type.Field(j))
			fields = append(fields, envField{
				key:   sectionTag + "." + leafTag,
				name:  envPrefix + strings.ToUpper(sectionTag) + "_" + strings.ToUpper(leafTag),
				index: []int{i, j},
			})
		}
	}
	return fields
}

func yamlTagName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	return name
}

// applyEnvOverrides overlays every AMDL_* variable in environ onto cfg. An
// AMDL_-prefixed variable that is neither a config key nor in envIgnored is
// an error, so a typo fails startup loudly instead of being skipped. Values
// are only parsed here; semantic checks stay in Validate, which callers run
// after the overlay.
func applyEnvOverrides(cfg *Config, environ []string) error {
	overrides := map[string]string{}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, envPrefix) {
			continue
		}
		if _, ok := envIgnored[name]; ok {
			continue
		}
		overrides[name] = value
	}
	if len(overrides) == 0 {
		return nil
	}
	cfgValue := reflect.ValueOf(cfg).Elem()
	for _, field := range envFields() {
		raw, ok := overrides[field.name]
		if !ok {
			continue
		}
		if err := setFromEnv(cfgValue.FieldByIndex(field.index), raw); err != nil {
			return fmt.Errorf("environment variable %s (%s): %w", field.name, field.key, err)
		}
		delete(overrides, field.name)
	}
	if len(overrides) > 0 {
		return fmt.Errorf("unknown configuration environment variable(s): %s", strings.Join(slices.Sorted(maps.Keys(overrides)), ", "))
	}
	return nil
}

func setFromEnv(target reflect.Value, raw string) error {
	switch target.Kind() {
	case reflect.String:
		target.SetString(raw)
	case reflect.Bool:
		parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid boolean %q", raw)
		}
		target.SetBool(parsed)
	case reflect.Int:
		parsed, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid integer %q", raw)
		}
		target.SetInt(int64(parsed))
	case reflect.Slice:
		target.Set(reflect.ValueOf(splitEnvList(raw)))
	default:
		return fmt.Errorf("unsupported field type %s", target.Kind())
	}
	return nil
}

// splitEnvList parses a comma-separated list value. Items are trimmed and
// empty items dropped, so trailing commas are harmless; an empty (or
// all-comma) value overrides the key to an empty list.
func splitEnvList(raw string) []string {
	items := []string{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// EnvLockedChanges returns a "key (VARIABLE)" entry for every field that
// differs between current and merged while its override variable is set in
// lookup. PUT /api/v1/config rejects such updates: the write itself would
// succeed, but the next Load would overlay the environment value again and
// silently shadow it.
func EnvLockedChanges(current, merged Config, lookup func(string) (string, bool)) []string {
	var locked []string
	currentValue := reflect.ValueOf(current)
	mergedValue := reflect.ValueOf(merged)
	for _, field := range envFields() {
		if _, ok := lookup(field.name); !ok {
			continue
		}
		if !reflect.DeepEqual(currentValue.FieldByIndex(field.index).Interface(), mergedValue.FieldByIndex(field.index).Interface()) {
			locked = append(locked, fmt.Sprintf("%s (%s)", field.key, field.name))
		}
	}
	return locked
}
