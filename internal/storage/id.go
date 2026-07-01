package storage

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_" + strings.ReplaceAll(hex.EncodeToString([]byte(prefix)), " ", "")
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
