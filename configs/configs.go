// Package configs embeds the shipped config example and hooks template, so a
// binary can seed a new install without the repository's files on disk.
package configs

import _ "embed"

// Example is config.example.yaml: every key with its documentation, and the
// file a missing config.yaml is created from (config.EnsureFile).
//
//go:embed config.example.yaml
var Example []byte

// Hooks is hooks.example.yaml, the commented hooks template.
//
//go:embed hooks.example.yaml
var Hooks []byte
