package domain

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a short random identifier with a readable prefix, e.g. "tsk_9f3a2b".
func NewID(prefix string) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable for a running agent; panicking here
		// is preferable to silently issuing colliding identifiers.
		panic("entropy unavailable: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b)
}
