// Package audit verifies the append-only audit chain.
//
// The chain is written by the store; this package only checks it. Verifying is a
// read-only operation so it can run during an incident without touching state.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"personal-agent/internal/domain"
)

// Verify walks the chain and reports the first break it finds.
func Verify(events []domain.AuditEvent) error {
	prev := ""
	for i, e := range events {
		if e.Seq != int64(i+1) {
			return fmt.Errorf("audit: sequence break at %d, got %d", i+1, e.Seq)
		}
		if e.PrevHash != prev {
			return fmt.Errorf("audit: prev hash mismatch at seq %d", e.Seq)
		}
		if e.Hash != hash(&e) {
			return fmt.Errorf("audit: hash mismatch at seq %d (payload was modified)", e.Seq)
		}
		prev = e.Hash
	}
	return nil
}

func hash(e *domain.AuditEvent) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s|%s|%s",
		e.Seq, e.At.Format(time.RFC3339Nano), e.Actor, e.Type, e.UserID, e.TaskID, e.IntentID, e.Payload, e.PrevHash)
	return hex.EncodeToString(h.Sum(nil))
}
