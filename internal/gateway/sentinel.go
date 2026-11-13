// Package gateway implements Sentinel: the single place that holds credentials,
// signs execution intents and decides what the model is allowed to do.
//
// Three rules are enforced here and nowhere else:
//  1. the planner never owns credentials, it only describes an intent;
//  2. the executor refuses anything without a valid, unexpired, single-use signature;
//  3. every decision is written to the audit chain before it takes effect.
package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"personal-agent/internal/domain"
	"personal-agent/internal/policy"
	"personal-agent/internal/store"
)

// Errors returned by the gateway. Callers map them to HTTP codes.
var (
	ErrForbidden    = errors.New("gateway: action forbidden")
	ErrBadSignature = errors.New("gateway: invalid signature")
	ErrExpired      = errors.New("gateway: intent expired")
	ErrReplay       = errors.New("gateway: nonce already used")
	ErrNoApproval   = errors.New("gateway: approval required")
)

// Sentinel is the permission gateway.
type Sentinel struct {
	st  *store.Store
	key []byte

	mu        sync.Mutex
	usedNonce map[string]time.Time

	ttl time.Duration
}

// New builds a Sentinel. ttl bounds how long a signed intent stays valid.
func New(st *store.Store, key []byte, ttl time.Duration) *Sentinel {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &Sentinel{st: st, key: key, usedNonce: map[string]time.Time{}, ttl: ttl}
}

// Authorize routes an intent by its permission class.
//
//   - read  -> signed immediately, audit written, allowed
//   - confirm -> approval created, nothing signed, caller must wait for a human
//   - forbidden -> rejected, audit written, no approval is even offered
//
// The risk class comes from the policy table, never from the caller: a planner
// cannot label a payment as "read" to skip approval.
func (s *Sentinel) Authorize(in *domain.Intent, trust string, amountCNY float64) (*domain.Decision, error) {
	risk := policy.RiskOf(in.Action)
	in.Risk = risk

	switch risk {
	case domain.RiskForbidden:
		s.audit("gateway", "intent_denied", in, "forbidden action: "+in.Action)
		return &domain.Decision{Allowed: false, Reason: "action is forbidden by policy"}, ErrForbidden

	case domain.RiskRead:
		if err := s.sign(in); err != nil {
			return nil, err
		}
		s.st.AddIntent(in)
		s.audit("gateway", "intent_signed", in, "read action auto-signed")
		return &domain.Decision{Allowed: true, IntentID: in.ID, Reason: "read action"}, nil

	default: // confirm
		if policy.AutoAllowedTier(trust, amountCNY) {
			if err := s.sign(in); err != nil {
				return nil, err
			}
			s.st.AddIntent(in)
			s.audit("gateway", "intent_signed", in, "auto tier2 low amount")
			return &domain.Decision{Allowed: true, IntentID: in.ID, Reason: "auto within tier2 limit"}, nil
		}
		ap := &domain.Approval{
			ID:          domain.NewID("apr"),
			IntentID:    in.ID,
			UserID:      in.UserID,
			Summary:     summary(in),
			Object:      in.Params["object"],
			AmountCNY:   amountCNY,
			RejectRisk:  rejectRisk(in),
			State:       domain.ApprovalPending,
			ContentHash: contentHash(in),
			CreatedAt:   time.Now().UTC(),
		}
		in.ApprovalID = ap.ID
		s.st.AddIntent(in)
		s.st.AddApproval(ap)
		s.audit("gateway", "approval_requested", in, ap.ID)
		return &domain.Decision{Allowed: false, NeedsHuman: true, IntentID: in.ID, ApprovalID: ap.ID, Reason: "approval required"}, nil
	}
}

// Grant signs the intent behind an approval. The approval content hash must still
// match, so a changed amount or object invalidates the human decision.
func (s *Sentinel) Grant(approvalID, userID string) (*domain.Intent, error) {
	ap, err := s.st.Approval(approvalID)
	if err != nil {
		return nil, err
	}
	if ap.UserID != userID {
		return nil, fmt.Errorf("gateway: approval belongs to another user")
	}
	if ap.State == domain.ApprovalExpired {
		return nil, ErrExpired
	}
	in, err := s.st.Intent(ap.IntentID)
	if err != nil {
		return nil, err
	}
	if contentHash(in) != ap.ContentHash {
		ap.State = domain.ApprovalExpired
		s.st.UpdateApproval(ap)
		s.audit("user", "approval_invalidated", in, "content changed after approval")
		return nil, fmt.Errorf("gateway: intent content changed, approval void")
	}
	if err := s.sign(in); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ap.State = domain.ApprovalGranted
	ap.Token = in.Signature
	ap.DecidedAt = &now
	s.st.UpdateApproval(ap)
	s.st.UpdateIntent(in)
	s.audit("user", "approval_granted", in, ap.ID)
	return in, nil
}

// Reject closes an approval without signing anything.
func (s *Sentinel) Reject(approvalID, userID, reason string) error {
	ap, err := s.st.Approval(approvalID)
	if err != nil {
		return err
	}
	if ap.UserID != userID {
		return fmt.Errorf("gateway: approval belongs to another user")
	}
	now := time.Now().UTC()
	ap.State = domain.ApprovalRejected
	ap.DecidedAt = &now
	ap.RejectRisk = reason
	s.st.UpdateApproval(ap)
	if in, err := s.st.Intent(ap.IntentID); err == nil {
		s.audit("user", "approval_rejected", in, reason)
	}
	return nil
}

// Verify is what the executor calls. It checks signature, expiry and single use,
// then burns the nonce. Any failure means the action must not run.
func (s *Sentinel) Verify(in *domain.Intent) error {
	if in.Signature == "" {
		return ErrNoApproval
	}
	want := s.signature(in)
	if !hmac.Equal([]byte(want), []byte(in.Signature)) {
		return ErrBadSignature
	}
	if time.Now().UTC().After(in.ExpiresAt) {
		return ErrExpired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, used := s.usedNonce[in.Nonce]; used {
		return ErrReplay
	}
	s.usedNonce[in.Nonce] = time.Now()
	// Opportunistic cleanup keeps the replay table bounded.
	for n, ts := range s.usedNonce {
		if time.Since(ts) > 2*s.ttl {
			delete(s.usedNonce, n)
		}
	}
	return nil
}

func (s *Sentinel) sign(in *domain.Intent) error {
	in.Nonce = domain.NewID("n")
	in.ExpiresAt = time.Now().UTC().Add(s.ttl)
	in.Signature = s.signature(in)
	return nil
}

func (s *Sentinel) signature(in *domain.Intent) string {
	m := hmac.New(sha256.New, s.key)
	fmt.Fprintf(m, "%s|%s|%s|%s|%s|%s|%d", in.ID, in.TaskID, in.RunID, in.Action, in.Risk, in.Nonce, in.ExpiresAt.Unix())
	if len(in.Params) > 0 {
		raw, _ := json.Marshal(in.Params)
		m.Write(raw)
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Sentinel) audit(actor, typ string, in *domain.Intent, detail string) {
	s.st.AppendAudit(actor, typ, in.UserID, in.TaskID, in.ID, detail)
}

func contentHash(in *domain.Intent) string {
	keys := make([]string, 0, len(in.Params))
	for k := range in.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(in.Action)
	for _, k := range keys {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(in.Params[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func summary(in *domain.Intent) string {
	amount, _ := strconv.ParseFloat(in.Params["amount"], 64)
	obj := in.Params["object"]
	if obj == "" {
		obj = in.Params["url"]
	}
	return fmt.Sprintf("%s / %s / %.2f CNY", in.Action, obj, amount)
}

func rejectRisk(in *domain.Intent) string {
	switch in.Action {
	case policy.ActPlaceOrder:
		return "拒绝后不会下单，任务保留监测"
	case policy.ActSubmitForm:
		return "拒绝后不提交表单，可稍后重试"
	default:
		return "拒绝后本次动作不执行，任务继续运行"
	}
}
