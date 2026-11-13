package gateway

import (
	"errors"
	"testing"
	"time"

	"personal-agent/internal/domain"
	"personal-agent/internal/policy"
	"personal-agent/internal/store"
)

func newSentinel(t *testing.T) (*Sentinel, *store.Store) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return New(st, []byte("test-key"), 15*time.Minute), st
}

func intent(action string) *domain.Intent {
	return &domain.Intent{
		ID:     "itn_1",
		TaskID: "tsk_1",
		RunID:  "run_1",
		UserID: "u1",
		Action: action,
		Params: map[string]string{"object": "headphone", "amount": "89"},
	}
}

func TestForbiddenActionIsRefused(t *testing.T) {
	gw, _ := newSentinel(t)
	dec, err := gw.Authorize(intent(policy.ActPay), "tier2", 89)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("pay must be forbidden regardless of trust, got err=%v dec=%+v", err, dec)
	}
	if dec == nil || dec.Allowed {
		t.Fatalf("forbidden action must not be allowed")
	}
}

func TestReadActionIsSignedAndSingleUse(t *testing.T) {
	gw, _ := newSentinel(t)
	in := intent(policy.ActFetchPrice)
	dec, err := gw.Authorize(in, "tier0", 0)
	if err != nil || dec == nil || !dec.Allowed {
		t.Fatalf("read action should be allowed: %v", err)
	}
	if in.Signature == "" {
		t.Fatal("read action must carry a signature")
	}
	if err := gw.Verify(in); err != nil {
		t.Fatalf("first verify should pass: %v", err)
	}
	if err := gw.Verify(in); !errors.Is(err, ErrReplay) {
		t.Fatalf("second verify must fail with replay, got %v", err)
	}
}

func TestExpiredIntentIsRejected(t *testing.T) {
	gw, _ := newSentinel(t)
	// A past TTL produces a correctly signed intent that is already expired; the
	// signature covers the expiry, so tampering with it is rejected as a bad signature.
	gw.ttl = -time.Minute
	in := intent(policy.ActFetchPrice)
	if _, err := gw.Authorize(in, "tier0", 0); err != nil {
		t.Fatal(err)
	}
	if err := gw.Verify(in); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired intent must be rejected, got %v", err)
	}
}

func TestConfirmRequiresApprovalWithinTierLimit(t *testing.T) {
	gw, _ := newSentinel(t)
	in := intent(policy.ActPlaceOrder)
	dec, err := gw.Authorize(in, "tier0", 89)
	if err != nil {
		t.Fatal(err)
	}
	if dec.NeedsHuman != true || dec.Allowed {
		t.Fatalf("tier0 confirm action must require a human, got %+v", dec)
	}
	if in.Signature != "" {
		t.Fatal("nothing may be signed before approval")
	}
	ap, err := gw.st.Approval(dec.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if ap.State != domain.ApprovalPending || ap.ContentHash == "" {
		t.Fatalf("approval must be pending with a content hash, got %+v", ap)
	}
}

func TestApprovalGrantsThenContentChangeVoids(t *testing.T) {
	gw, st := newSentinel(t)
	in := intent(policy.ActPlaceOrder)
	dec, err := gw.Authorize(in, "tier0", 89)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := gw.Grant(dec.ApprovalID, "u1")
	if err != nil {
		t.Fatalf("grant failed: %v", err)
	}
	if signed.Signature == "" {
		t.Fatal("granted intent must be signed")
	}
	if err := gw.Verify(signed); err != nil {
		t.Fatalf("granted intent should verify: %v", err)
	}

	// Same intent, changed amount after the user already approved.
	in2 := intent(policy.ActPlaceOrder)
	dec2, err := gw.Authorize(in2, "tier0", 89)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := st.Intent(in2.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Params["amount"] = "890"
	st.UpdateIntent(stored)
	if _, err := gw.Grant(dec2.ApprovalID, "u1"); err == nil {
		t.Fatal("changing content after approval must void the approval")
	}
}

func TestAutoTierAllowsSmallAmountOnly(t *testing.T) {
	if policy.AutoAllowedTier("tier2", 50) != true {
		t.Fatal("tier2 within limit should auto allow")
	}
	if policy.AutoAllowedTier("tier2", 500) != false {
		t.Fatal("tier2 above limit must not auto allow")
	}
	if policy.AutoAllowedTier("tier1", 10) != false {
		t.Fatal("only tier2 may auto allow")
	}
}
