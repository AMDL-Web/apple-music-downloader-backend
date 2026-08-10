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
// AMDL_DOWNLOAD_QUALITY_PRIORITY. The environment is the highest layer: it
// beats configs/config.yaml, which beats the database, which beats the
// built-in defaults. Nothing is ever written back to the environment, and a
// key an AMDL_* variable pins cannot be changed through PUT /api/v1/config.
// Value syntax: strings verbatim, booleans per strconv.ParseBool, integers as
// digits, string lists as comma-separated items (an empty value is an empty
// list).

// envPrefix is the shared prefix of every backend environment variable.
const envPrefix = "AMDL_"

// envIgnored are AMDL_-prefixed variables that are not config-key overrides:
// the file-path variables read in main.
var envIgnored = map[string]struct{}{
	"AMDL_CONFIG":       {},
	"AMDL_HOOKS_CONFIG": {},
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

// envOverrides resolves the AMDL_* variables in environ to dotted config keys
// and their raw values. An AMDL_-prefixed variable that is neither a config
// key nor in envIgnored is an error, so a typo fails startup loudly instead of
// being skipped silently.
func envOverrides(environ []string) (map[string]string, error) {
	present := map[string]string{}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, envPrefix) {
			continue
		}
		if _, ok := envIgnored[name]; ok {
			continue
		}
		present[name] = value
	}
	overrides := map[string]string{}
	for _, field := range envFields() {
		if raw, ok := present[field.name]; ok {
			overrides[field.key] = raw
			delete(present, field.name)
		}
	}
	if len(present) > 0 {
		return nil, fmt.Errorf("unknown configuration environment variable(s): %s", strings.Join(slices.Sorted(maps.Keys(present)), ", "))
	}
	return overrides, nil
}

// applyEnvOverrides sets every key in overrides on cfg. Values are only parsed
// here; semantic checks stay in Validate, which Resolve runs afterwards.
func applyEnvOverrides(cfg *Config, overrides map[string]string) error {
	if len(overrides) == 0 {
		return nil
	}
	cfgValue := reflect.ValueOf(cfg).Elem()
	for _, field := range envFields() {
		raw, ok := overrides[field.key]
		if !ok {
			continue
		}
		if err := setFromEnv(cfgValue.FieldByIndex(field.index), raw); err != nil {
			return fmt.Errorf("environment variable %s (%s): %w", field.name, field.key, err)
		}
	}
	return nil
}

// EnvVarName returns the AMDL_* variable that pins a dotted config key, so
// error messages can name the variable an operator has to unset.
func EnvVarName(key string) string {
	for _, field := range envFields() {
		if field.key == key {
			return field.name
		}
	}
	return ""
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
