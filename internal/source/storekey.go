package source

import "github.com/LLM-4-People/mailsense/internal/digest"

// StoreKey is the stable key of one mailbox store of one source:
// "store-" + 12 hex digits of the SHA-256 of the source id and the store
// id. A store id survives renames and restarts, a display name is
// localized and editable, and no store has a special key: which store is
// the profile's default is a property the user sees when picking
// (Mailbox.Default), never a key that could silently point at another
// store. Every per-store setting uses this one key.
func StoreKey(sourceID, storeID string) string {
	return storeKeyPrefix + digest.Hex([]byte(sourceID+"\x00"+storeID), storeKeyDigits)
}

// ValidStoreKey reports whether key has exactly the form StoreKey produces.
// It is the one syntax check of a store key handed in by a client.
func ValidStoreKey(key string) bool {
	if len(key) != len(storeKeyPrefix)+storeKeyDigits || key[:len(storeKeyPrefix)] != storeKeyPrefix {
		return false
	}
	for _, c := range key[len(storeKeyPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// The key format; schema.json's per-store list patterns match it
// (TestStoreKeyMatchesTheSchemaPattern).
const (
	storeKeyPrefix = "store-"
	storeKeyDigits = 12 // collisions need ~16 M stores
)
