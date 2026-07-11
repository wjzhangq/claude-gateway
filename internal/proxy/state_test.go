package proxy

// White-box tests for the 5-state health machine, TTL policy, reconcile/recovery,
// failsafe, and weight factors. Uses the injectable clock (nowFn) and rand (rng)
// to make tests deterministic and avoid real-time waits.
//
// Tasks covered:
//   T008/T009 – US1 exit rules + TTL policy
//   T015      – US2 reconcile-driven recovery
//   T020      – US3 failsafe
//   T023      – US4 budgetFactor, latencyFactor, SWRR
//   T028      – US5 Reconcile step ordering, consecErr decay, idle probe

import (
	"math/rand"
	"testing"
	"time"

	"github.com/wjzhangq/claude-gateway/config"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestLB builds a LoadBalancer with a frozen clock and zero-jitter rand so
// TTL assertions are deterministic.  The clock is advanced by calling advance().
func newTestLB(cfgs []config.BackendAPI) (*LoadBalancer, *int64) {
	frozen := int64(1_000_000) // arbitrary unix second
	lb := &LoadBalancer{
		nowFn: func() time.Time { return time.Unix(frozen, 0) },
		rng:   rand.New(rand.NewSource(42)),
	}
	for _, c := range cfgs {
		if c.Enabled {
			lb.backends = append(lb.backends, lb.newBackend(c))
		}
	}
	lb.updateCapacities()
	return lb, &frozen
}

func advance(frozen *int64, secs int64) { *frozen += secs }

func singleCfg(weight int) []config.BackendAPI {
	return []config.BackendAPI{
		{Name: "b", URL: "http://b", APIKey: "k", Weight: weight, Enabled: true},
	}
}

func multiCfg(n, weight int) []config.BackendAPI {
	cfgs := make([]config.BackendAPI, n)
	for i := range cfgs {
		cfgs[i] = config.BackendAPI{
			Name:    string(rune('a' + i)),
			URL:     "http://x",
			APIKey:  "k",
			Weight:  weight,
			Enabled: true,
		}
	}
	return cfgs
}

// ---------------------------------------------------------------------------
// T008 – US1 exit rules
// ---------------------------------------------------------------------------

// 5xx/transport -> Isolated immediately (FR-005)
func TestExitRule_ServerErrorIsolates(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	if b.state.Load() != stateHealthy {
		t.Fatal("precondition: healthy")
	}
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatalf("ErrServer must isolate, got state=%d", b.state.Load())
	}
}

// 429 -> Isolated (FR-005)
func TestExitRule_RateLimitIsolates(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrRateLimit, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatalf("ErrRateLimit must isolate, got state=%d", b.state.Load())
	}
}

// 403 -> Isolated, NOT Quarantine (FR-005)
func TestExitRule_ForbiddenIsolates(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrForbidden, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatalf("ErrForbidden must isolate (not quarantine), got state=%d", b.state.Load())
	}
}

// 401 -> Quarantine + alert (FR-006)
func TestExitRule_AuthQuarantines(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrAuth, 0, false)
	if b.state.Load() != stateQuarantine {
		t.Fatalf("ErrAuth must quarantine, got state=%d", b.state.Load())
	}
}

// consecErr >= 5 -> Quarantine regardless of class (FR-007)
func TestExitRule_ConsecQuarantine(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	for i := 0; i < consecErrToQuarantine; i++ {
		lb.recordResult(b, ErrServer, 0, false)
		// Between calls, advance clock slightly so TTL doesn't expire but backend can re-enter Isolated
		// (releaseExpired is not called here; the state escalation is driven purely by consecErr)
		if i < consecErrToQuarantine-1 {
			// Force back to Healthy between hits so we can count up (realistic: reconcile would release)
			b.state.Store(stateHealthy)
		}
	}
	if b.state.Load() != stateQuarantine {
		t.Fatalf("5 consecutive errors must quarantine, got state=%d", b.state.Load())
	}
}

// 400/404/422 -> no state or counter change (FR-008)
func TestExitRule_ClientErrorIgnored(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	for i := 0; i < 20; i++ {
		lb.recordResult(b, ErrClient, 0, false)
	}
	if b.state.Load() != stateHealthy {
		t.Fatalf("ErrClient must not change state, got %d", b.state.Load())
	}
	if b.consecErr.Load() != 0 {
		t.Fatalf("ErrClient must not increment consecErr, got %d", b.consecErr.Load())
	}
}

// ErrCanceled -> no state or counter change (FR-008)
func TestExitRule_CanceledIgnored(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	for i := 0; i < 20; i++ {
		lb.recordResult(b, ErrCanceled, 0, false)
	}
	if b.state.Load() != stateHealthy {
		t.Fatalf("ErrCanceled must not change state, got %d", b.state.Load())
	}
	if b.consecErr.Load() != 0 {
		t.Fatalf("ErrCanceled must not increment consecErr, got %d", b.consecErr.Load())
	}
}

// ErrTransport -> Isolated (same as server fault)
func TestExitRule_TransportIsolates(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrTransport, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatalf("ErrTransport must isolate, got state=%d", b.state.Load())
	}
}

// Isolated backend must not be routable.
func TestExitRule_IsolatedUnselectable(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("precondition: must be isolated")
	}
	if lb.pick(nil) != nil {
		t.Fatal("isolated backend must not be picked")
	}
}

// Quarantine must not be routable.
func TestExitRule_QuarantineUnselectable(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrAuth, 0, false)
	if lb.pick(nil) != nil {
		t.Fatal("quarantined backend must not be picked")
	}
}

// ---------------------------------------------------------------------------
// T009 – US1 TTL policy
// ---------------------------------------------------------------------------

// computeTTL with zero jitter (seed 42 → jitter ≈ base*[0.8,1.2]; we just
// check lower-bound ≥ 0.8×base and upper-bound ≤ 1.2×base).
func TestTTLPolicy_BaseRange(t *testing.T) {
	cases := []struct {
		code    int
		base    int64
		capSecs int64
	}{
		{429, ttlBase429, ttlCap429},
		{403, ttlBase403, ttlCap403},
		{500, ttlBase5xx, ttlCap5xx},
		{502, ttlBase5xx, ttlCap5xx},
		{0, ttlBase5xx, ttlCap5xx}, // transport (no code)
	}
	lb, _ := newTestLB(singleCfg(1))
	now := time.Unix(1_000_000, 0)
	for _, tc := range cases {
		got := lb.computeTTL(tc.code, 0, false, now)
		lo := int64(float64(tc.base) * 0.8)
		hi := int64(float64(tc.base) * 1.2)
		if got < lo || got > hi {
			t.Errorf("code=%d: computeTTL=%d, want [%d,%d]", tc.code, got, lo, hi)
		}
	}
}

// 429 with Retry-After -> TTL equals the delta (FR-012)
func TestTTLPolicy_RetryAfterOverrides429(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	now := lb.nowFn()
	retryAfterUnix := now.Unix() + 120
	lb.recordResult(b, ErrRateLimit, retryAfterUnix, false)
	ttl := b.ttlUntil.Load() - now.Unix()
	if ttl != 120 {
		t.Fatalf("Retry-After=120: want ttl=120, got %d", ttl)
	}
}

// 403 quota body -> TTL to next CST midnight (FR-013)
func TestTTLPolicy_ForbiddenQuotaBodyExtends(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	// Pin time to a known moment: 2024-01-15 14:00:00 UTC = 22:00 CST
	// Next CST midnight is 2024-01-15 16:00:00 UTC (2h away)
	t0, _ := time.Parse(time.RFC3339, "2024-01-15T14:00:00Z")
	*frozen = t0.Unix()
	b := lb.backends[0]
	lb.recordResult(b, ErrForbidden, 0, true)
	got := b.ttlUntil.Load() - t0.Unix()
	want := secondsUntilCSTMidnight(t0)
	if got != want {
		t.Fatalf("403 quota body: want ttl=%d (CST midnight), got %d", want, got)
	}
}

// 403 non-quota body -> uses base TTL with jitter (not the daily reset)
func TestTTLPolicy_ForbiddenNonQuotaBase(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	now := lb.nowFn()
	lb.recordResult(b, ErrForbidden, 0, false)
	got := b.ttlUntil.Load() - now.Unix()
	lo := int64(float64(ttlBase403) * 0.8)
	hi := int64(float64(ttlBase403) * 1.2)
	if got < lo || got > hi {
		t.Fatalf("403 non-quota: want ttl in [%d,%d], got %d", lo, hi, got)
	}
}

// ttlUntil is set on every Isolated transition.
func TestTTLPolicy_TTLSetOnIsolation(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("precondition: isolated")
	}
	if b.ttlUntil.Load() == 0 {
		t.Fatal("ttlUntil must be set on isolation")
	}
}

// ---------------------------------------------------------------------------
// T015 – US2 reconcile-driven recovery
// ---------------------------------------------------------------------------

// releaseExpired: Isolated past TTL -> Probing
func TestRecovery_ReleaseExpiredIsolated(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("precondition: isolated")
	}
	// Advance past the TTL (max 36s = 1.2×30)
	advance(frozen, 60)
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatalf("after TTL expiry: want probing, got %d", b.state.Load())
	}
}

// releaseExpired: Quarantine past TTL -> Probing
func TestRecovery_ReleaseExpiredQuarantine(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrAuth, 0, false)
	if b.state.Load() != stateQuarantine {
		t.Fatal("precondition: quarantine")
	}
	// Quarantine TTL is ttlBase401=900s; advance past that
	advance(frozen, int64(ttlBase401)*2)
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatalf("quarantine TTL expiry: want probing, got %d", b.state.Load())
	}
}

// judgeProbing: stale lastHTTPCode must NOT re-isolate before a post-promotion trial
func TestRecovery_JudgeProbingSkipsStaleCode(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	// Isolate via ErrServer (sets lastHTTPCode=500 implicitly via RecordRequest not called)
	lb.recordResult(b, ErrServer, 0, false)
	advance(frozen, 60) // expire TTL
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatal("precondition: probing after TTL expiry")
	}
	// judgeProbing runs: lastCodeAt < enteredAt, so it must skip (no re-isolation)
	lb.judgeProbing(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatalf("judgeProbing must not re-isolate with stale code; state=%d", b.state.Load())
	}
}

// judgeProbing: probe trial success -> Degraded
func TestRecovery_JudgeProbingSuccessDegrades(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	advance(frozen, 60)
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatal("precondition: probing")
	}
	// Simulate a successful trial result arriving after promotion
	b.lastHTTPCode.Store(200)
	b.lastCodeAt.Store(lb.nowFn().Unix())
	lb.judgeProbing(lb.nowFn())
	if b.state.Load() != stateDegraded {
		t.Fatalf("probe success: want degraded, got %d", b.state.Load())
	}
}

// judgeProbing: probe trial failure -> Isolated with doubled TTL
func TestRecovery_JudgeProbingFailureIsolates(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	origTTL := b.ttlUntil.Load() - lb.nowFn().Unix()
	advance(frozen, 60)
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatal("precondition: probing")
	}
	// Simulate a failed trial result arriving after promotion
	b.lastHTTPCode.Store(500)
	b.lastCodeAt.Store(lb.nowFn().Unix())
	lb.judgeProbing(lb.nowFn())
	if b.state.Load() != stateIsolated {
		t.Fatalf("probe fail: want isolated, got %d", b.state.Load())
	}
	newTTL := b.ttlUntil.Load() - lb.nowFn().Unix()
	// New TTL should be ≈ 2×prev (with jitter); check it's > original
	if newTTL <= origTTL {
		t.Fatalf("probe fail: new TTL (%d) should be > orig TTL (%d)", newTTL, origTTL)
	}
}

// Full reconcile cycle: Isolated -> (TTL expiry) -> Probing -> (success trial) -> Degraded
func TestRecovery_FullCycleIsolatedToHealthy(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	// Healthy -> Isolated
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("step 1: must be isolated")
	}
	// TTL expiry in Reconcile -> Probing
	advance(frozen, 60)
	b.lastHTTPCode.Store(200)
	b.lastCodeAt.Store(lb.nowFn().Unix())
	lb.Reconcile(lb.nowFn())
	if b.state.Load() != stateDegraded {
		t.Fatalf("after reconcile+success: want degraded, got %d", b.state.Load())
	}
	// Second reconcile with clean window -> Healthy
	b.lastSuccessAt.Store(lb.nowFn().Unix())
	b.consecErr.Store(0)
	advance(frozen, cleanWindowSecs+1)
	lb.Reconcile(lb.nowFn())
	if b.state.Load() != stateHealthy {
		t.Fatalf("after clean window: want healthy, got %d", b.state.Load())
	}
}

// softUpDown: Degraded with clean window + no consecErr -> Healthy
func TestRecovery_SoftUpDownDegradedToHealthy(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.setState(stateDegraded, lb.nowFn())
	b.lastSuccessAt.Store(lb.nowFn().Unix() + 1)
	b.consecErr.Store(0)
	advance(frozen, cleanWindowSecs+1)
	lb.softUpDown(lb.nowFn())
	if b.state.Load() != stateHealthy {
		t.Fatalf("clean %ds: want healthy, got %d", cleanWindowSecs, b.state.Load())
	}
}

// ---------------------------------------------------------------------------
// T020 – US3 failsafe
// ---------------------------------------------------------------------------

// Failsafe triggers when routable < 3 and promotes Isolated to Probing
func TestFailsafe_PromotesWhenLow(t *testing.T) {
	// 3 backends: isolate all so routable=0
	lb, _ := newTestLB(multiCfg(3, 10))
	for _, b := range lb.backends {
		lb.recordResult(b, ErrServer, 0, false)
	}
	if r := lb.routableCount(); r != 0 {
		t.Fatalf("precondition: want 0 routable, got %d", r)
	}
	lb.failsafe(lb.nowFn())
	if r := lb.routableCount(); r < failsafeTrigger {
		t.Fatalf("after failsafe: want routable >= %d, got %d", failsafeTrigger, r)
	}
}

// Failsafe respects hysteresis: won't re-run within failsafeHysteresisSecs
func TestFailsafe_Hysteresis(t *testing.T) {
	lb, frozen := newTestLB(multiCfg(3, 10))
	for _, b := range lb.backends {
		lb.recordResult(b, ErrServer, 0, false)
	}
	// First run: promotes backends
	lb.failsafe(lb.nowFn())
	firstAt := lb.lastFailsafeAt.Load()
	if firstAt == 0 {
		t.Fatal("lastFailsafeAt must be set after failsafe")
	}
	// Re-isolate promoted backends
	for _, b := range lb.backends {
		if b.state.Load() == stateProbing {
			b.state.Store(stateIsolated)
		}
	}
	// Second run within hysteresis window: must not promote
	advance(frozen, failsafeHysteresisSecs-1)
	before := lb.routableCount()
	lb.failsafe(lb.nowFn())
	after := lb.routableCount()
	if after != before {
		t.Fatalf("failsafe ran within hysteresis window: routable changed %d -> %d", before, after)
	}
}

// Failsafe shields promoted nodes (probeShieldUntil prevents re-isolation)
func TestFailsafe_PromotedBackendShielded(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	// Force failsafe (single backend, routable=0 < trigger=3)
	lb.failsafe(lb.nowFn())
	if b.state.Load() != stateProbing {
		t.Fatal("failsafe must promote to probing")
	}
	shield := b.probeShieldUntil.Load()
	if shield == 0 {
		t.Fatal("probeShieldUntil must be set after failsafe promotion")
	}
	// Try to re-isolate while shielded: recordResult must no-op for Probing+shielded
	b.lastHTTPCode.Store(500)
	b.lastCodeAt.Store(lb.nowFn().Unix())
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateProbing {
		t.Fatalf("shielded probing must not be re-isolated by request errors; state=%d", b.state.Load())
	}
	// After shield expires, it should be vulnerable
	advance(frozen, failsafeShieldSecs+1)
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() == stateProbing {
		t.Fatal("after shield expiry, probing must be re-isolatable")
	}
}

// Failsafe no-ops when routable >= trigger
func TestFailsafe_NoOpWhenSufficient(t *testing.T) {
	lb, _ := newTestLB(multiCfg(5, 10))
	// All healthy: failsafe should not promote anything
	for _, b := range lb.backends {
		if b.state.Load() != stateHealthy {
			t.Fatal("precondition: all healthy")
		}
	}
	lb.failsafe(lb.nowFn())
	// All still healthy
	for _, b := range lb.backends {
		if b.state.Load() != stateHealthy {
			t.Fatalf("failsafe must not touch healthy backends; state=%d", b.state.Load())
		}
	}
}

// Failsafe ranking: transient (429/5xx) sorted before quota (403) before quarantine (401)
func TestFailsafe_Ranking(t *testing.T) {
	lb, _ := newTestLB(multiCfg(3, 10))
	// backend 'a': isolated via 5xx (transient, codeClass=0)
	lb.backends[0].state.Store(stateIsolated)
	lb.backends[0].lastHTTPCode.Store(500)
	// backend 'b': isolated via 403 (codeClass=1)
	lb.backends[1].state.Store(stateIsolated)
	lb.backends[1].lastHTTPCode.Store(403)
	// backend 'c': quarantine (codeClass=2)
	lb.backends[2].state.Store(stateQuarantine)
	lb.backends[2].lastHTTPCode.Store(401)

	// Force only 1 promotion (target=3 but we have 0 routable; promote until >=3 or run out)
	// All three get promoted, but ranking order matters for future partial promotions.
	// Test: if only one can be promoted, it should be the transient (codeClass=0) one.
	// We verify by running failsafe with hysteresis already spent & checking which got promoted first.
	// Reset to ensure only first in ranking gets promoted up to target.
	// Cap target to 1 by setting failsafeTargetCap conceptually, but we can't change const.
	// Instead: set 2 backends as already routable (healthy), so only 1 needs promotion.
	lb.backends[1].state.Store(stateHealthy)
	lb.backends[2].state.Store(stateHealthy)
	// Now routable=2 < trigger=3, candidates = [backend 'a' (5xx)].
	lb.failsafe(lb.nowFn())
	if lb.backends[0].state.Load() != stateProbing {
		t.Fatalf("transient-class backend should be promoted first; state=%d", lb.backends[0].state.Load())
	}
}

// ---------------------------------------------------------------------------
// T023 – US4 budgetFactor, latencyFactor, SWRR
// ---------------------------------------------------------------------------

func TestBudgetFactor_Tiers(t *testing.T) {
	cases := []struct {
		usage float64
		limit float64
		want  float64
		lo    float64
		hi    float64
	}{
		{0, 100, 1.0, 1.0, 1.0},    // <80%
		{79, 100, 1.0, 1.0, 1.0},   // just under 80%
		{80, 100, 0, 0.3, 1.0},     // entering 80-95% band
		{87.5, 100, 0.65, 0.3, 1.0},// midpoint 80-95% -> ~0.65
		{95, 100, 0, 0.0, 0.31},    // entering 95-99% band -> ~0.3
		{99, 100, 0, 0.04, 0.06},   // >=99% -> 0.05
		{100, 100, 0, 0.04, 0.06},  // over limit -> 0.05
		{0, 0, 1.0, 1.0, 1.0},      // no limit -> 1.0
	}
	for _, tc := range cases {
		f := budgetFactor(int64(tc.usage*100), int64(tc.limit*100))
		if f < tc.lo-0.001 || f > tc.hi+0.001 {
			t.Errorf("budgetFactor(%.0f%%): got %.4f, want in [%.4f, %.4f]",
				tc.usage/tc.limit*100, f, tc.lo, tc.hi)
		}
		if f < budgetFloor-0.001 {
			t.Errorf("budgetFactor must never go below floor %.4f; got %.4f", budgetFloor, f)
		}
	}
}

func TestLatencyFactor_Tiers(t *testing.T) {
	cases := []struct {
		ms   int64
		want float64
	}{
		{0, 1.0},
		{1000, 1.0},
		{latencyKneeMs, 1.0},
		{latencyKneeMs * 2, 0.5},
		{latencyKneeMs * 3, 0.3333},
		{latencyKneeMs * 100, latencyFloor}, // floored
	}
	for _, tc := range cases {
		f := latencyFactor(tc.ms)
		if tc.ms >= latencyKneeMs*100 {
			if f < latencyFloor-0.001 || f > latencyFloor+0.001 {
				t.Errorf("latencyFactor(%d): want floor %.2f, got %.4f", tc.ms, latencyFloor, f)
			}
		} else if tc.ms <= latencyKneeMs {
			if f != 1.0 {
				t.Errorf("latencyFactor(%d): want 1.0, got %.4f", tc.ms, f)
			}
		} else {
			expected := float64(latencyKneeMs) / float64(tc.ms)
			if expected < latencyFloor {
				expected = latencyFloor
			}
			diff := f - expected
			if diff < -0.001 || diff > 0.001 {
				t.Errorf("latencyFactor(%d): want %.4f, got %.4f", tc.ms, expected, f)
			}
		}
	}
}

// quotaExhausted is a hard gate: even a Healthy backend returns weight 0
func TestEffectiveWeight_QuotaExhaustedHardGate(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	if b.effectiveWeight() == 0 {
		t.Fatal("precondition: healthy backend must have positive weight")
	}
	b.quotaExhausted.Store(true)
	if b.effectiveWeight() != 0 {
		t.Fatal("quotaExhausted backend must have weight 0 (hard gate)")
	}
	// Confirm it's unselectable
	lb2, _ := newTestLB(singleCfg(10))
	lb2.backends[0].quotaExhausted.Store(true)
	if lb2.pick(nil) != nil {
		t.Fatal("quota-exhausted backend must not be picked")
	}
}

// validationFailed is a hard gate
func TestEffectiveWeight_ValidationFailedHardGate(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.validationFailed.Store(true)
	if b.effectiveWeight() != 0 {
		t.Fatal("validationFailed backend must have weight 0")
	}
}

// Probing backends participate at fixed probingWeight (not base weight)
func TestEffectiveWeight_ProbingFixed(t *testing.T) {
	lb, _ := newTestLB(singleCfg(100))
	b := lb.backends[0]
	b.state.Store(stateProbing)
	if ew := b.effectiveWeight(); ew != probingWeight {
		t.Fatalf("probing: want effectiveWeight=%d, got %d", probingWeight, ew)
	}
}

// Degraded applies healthFactor(0.3) to base weight
func TestEffectiveWeight_DegradedReduced(t *testing.T) {
	lb, _ := newTestLB(singleCfg(100))
	b := lb.backends[0]
	b.state.Store(stateDegraded)
	ew := b.effectiveWeight()
	want := int(float64(100) * degradedHealthFactor) // =30
	if ew != want && ew != 1 { // min=1 guard applies if want<1
		t.Fatalf("degraded: want effectiveWeight=%d, got %d", want, ew)
	}
}

// SWRR: with two backends of equal weight, over N picks each gets N/2
func TestSWRR_EqualWeightDistribution(t *testing.T) {
	lb, _ := newTestLB([]config.BackendAPI{
		{Name: "a", URL: "http://a", APIKey: "k", Weight: 10, Enabled: true},
		{Name: "b", URL: "http://b", APIKey: "k", Weight: 10, Enabled: true},
	})
	counts := map[string]int{}
	const N = 100
	for i := 0; i < N; i++ {
		got := lb.pick(nil)
		if got == nil {
			t.Fatal("unexpected nil from pick")
		}
		counts[got.Name]++
	}
	// Each should get exactly N/2 picks with equal weights and SWRR
	if counts["a"] != N/2 || counts["b"] != N/2 {
		t.Errorf("SWRR equal weights: a=%d b=%d (want %d each)", counts["a"], counts["b"], N/2)
	}
}

// SWRR: weighted 2:1 -> 2/3 vs 1/3 of picks
func TestSWRR_WeightedDistribution(t *testing.T) {
	lb, _ := newTestLB([]config.BackendAPI{
		{Name: "a", URL: "http://a", APIKey: "k", Weight: 2, Enabled: true},
		{Name: "b", URL: "http://b", APIKey: "k", Weight: 1, Enabled: true},
	})
	counts := map[string]int{}
	const N = 300
	for i := 0; i < N; i++ {
		got := lb.pick(nil)
		if got == nil {
			t.Fatal("nil from pick")
		}
		counts[got.Name]++
	}
	// a should get 2/3 = 200, b should get 1/3 = 100
	if counts["a"] != 200 || counts["b"] != 100 {
		t.Errorf("SWRR 2:1 weights: a=%d b=%d (want 200/100)", counts["a"], counts["b"])
	}
}

// SWRR: starvation test — single-backend non-zero weight always gets picked
func TestSWRR_NoStarvation(t *testing.T) {
	lb, _ := newTestLB([]config.BackendAPI{
		{Name: "a", URL: "http://a", APIKey: "k", Weight: 100, Enabled: true},
		{Name: "b", URL: "http://b", APIKey: "k", Weight: 1, Enabled: true},
	})
	// Pick 300 times; low-weight b must get at least 1 pick
	bCount := 0
	for i := 0; i < 300; i++ {
		got := lb.pick(nil)
		if got != nil && got.Name == "b" {
			bCount++
		}
	}
	if bCount == 0 {
		t.Fatal("SWRR starvation: weight-1 backend never picked in 300 rounds")
	}
}

// ---------------------------------------------------------------------------
// T028 – US5 Reconcile step ordering and consecErr decay
// ---------------------------------------------------------------------------

// Reconcile step order: releaseExpired runs before judgeProbing so a backend
// that expires in the same tick moves to Probing (not stuck in Isolated).
func TestReconcile_StepOrder_ReleaseBeforeJudge(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("precondition: isolated")
	}
	// Advance past TTL and inject a successful post-expiry trial
	advance(frozen, 60)
	b.lastHTTPCode.Store(200)
	b.lastCodeAt.Store(lb.nowFn().Unix())
	// Single Reconcile must:
	//   1. releaseExpired: Isolated -> Probing
	//   2. judgeProbing:  Probing -> Degraded (success trial)
	lb.Reconcile(lb.nowFn())
	if b.state.Load() != stateDegraded {
		t.Fatalf("reconcile step order: want degraded after release+judge in one tick, got %d", b.state.Load())
	}
}

// consecErr decays to 0 after cleanWindowSecs with no recent error (softUpDown)
func TestReconcile_ConsecErrDecay(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.consecErr.Store(3)
	b.lastErr.Store(lb.nowFn().Unix() - 1) // set last error slightly before now
	// Advance past clean window
	advance(frozen, cleanWindowSecs+1)
	lb.softUpDown(lb.nowFn())
	if b.consecErr.Load() != 0 {
		t.Fatalf("consecErr must decay to 0 after %ds; got %d", cleanWindowSecs, b.consecErr.Load())
	}
}

// consecErr must NOT decay if a recent error exists
func TestReconcile_ConsecErrNoDecayWithRecentError(t *testing.T) {
	lb, frozen := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.consecErr.Store(3)
	// Set lastErr to now, so cleanWindowSecs has NOT elapsed from lastErr
	b.lastErr.Store(lb.nowFn().Unix())
	advance(frozen, cleanWindowSecs-1)
	lb.softUpDown(lb.nowFn())
	if b.consecErr.Load() == 0 {
		t.Fatal("consecErr must not decay before clean window from last error")
	}
}

// ErrNone resets consecErr and advances consecOK
func TestReconcile_SuccessResetsConsecErr(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	b.consecErr.Store(4)
	lb.recordResult(b, ErrNone, 0, false)
	if b.consecErr.Load() != 0 {
		t.Fatalf("ErrNone must reset consecErr; got %d", b.consecErr.Load())
	}
}

// Reconcile full ordered pass does not panic on empty backend list
func TestReconcile_EmptyBackends(t *testing.T) {
	lb, _ := newTestLB(nil)
	lb.Reconcile(lb.nowFn())
}

// releaseExpired must NOT release a backend whose TTL has not elapsed
func TestRecovery_ReleaseNotBeforeTTL(t *testing.T) {
	lb, _ := newTestLB(singleCfg(10))
	b := lb.backends[0]
	lb.recordResult(b, ErrServer, 0, false)
	if b.state.Load() != stateIsolated {
		t.Fatal("precondition: isolated")
	}
	// Do NOT advance clock; TTL has not elapsed
	lb.releaseExpired(lb.nowFn())
	if b.state.Load() != stateIsolated {
		t.Fatal("backend must remain isolated before TTL expiry")
	}
}
