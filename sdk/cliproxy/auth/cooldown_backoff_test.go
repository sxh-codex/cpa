package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func withQuotaCooldownEnabled(t *testing.T) {
	t.Helper()
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })
}

func quotaResult(authID, model string) Result {
	return Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit",
			Message:    "quota",
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	}
}

func TestMarkResultQuota429CoolsDownOnEleventhAndSuccessClears(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-quota-eleventh",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	for i := 1; i <= quota429CooldownThreshold-1; i++ {
		manager.MarkResult(context.Background(), quotaResult(auth.ID, "gpt-5"))
		updated, ok := manager.GetByID(auth.ID)
		if !ok || updated == nil || updated.ModelStates["gpt-5"] == nil {
			t.Fatalf("expected model state after failure %d", i)
		}
		state := updated.ModelStates["gpt-5"]
		if state.Quota.BackoffLevel != i {
			t.Fatalf("failure %d BackoffLevel = %d, want %d", i, state.Quota.BackoffLevel, i)
		}
		if !state.NextRetryAfter.IsZero() || !state.Quota.NextRecoverAt.IsZero() {
			t.Fatalf("failure %d cooled down early: next=%v quota=%v", i, state.NextRetryAfter, state.Quota.NextRecoverAt)
		}
		if state.Quota.Exceeded {
			t.Fatalf("failure %d marked model quota exceeded before threshold: %+v", i, state.Quota)
		}
	}

	manager.MarkResult(context.Background(), quotaResult(auth.ID, "gpt-5"))
	cooled, ok := manager.GetByID(auth.ID)
	if !ok || cooled == nil || cooled.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after eleventh failure")
	}
	state := cooled.ModelStates["gpt-5"]
	if state.Quota.BackoffLevel < quota429CooldownThreshold {
		t.Fatalf("eleventh BackoffLevel = %d, want >= %d", state.Quota.BackoffLevel, quota429CooldownThreshold)
	}
	if !state.NextRetryAfter.After(time.Now()) || !state.Quota.NextRecoverAt.After(time.Now()) {
		t.Fatalf("eleventh did not cool down: next=%v quota=%v", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true})
	recovered, ok := manager.GetByID(auth.ID)
	if !ok || recovered == nil || recovered.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after success")
	}
	state = recovered.ModelStates["gpt-5"]
	if state.Quota.BackoffLevel != 0 || !state.NextRetryAfter.IsZero() || !state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("success did not clear 429 state: %+v", state)
	}
}

func TestApplyAuthFailureStateQuota429CoolsDownOnEleventh(t *testing.T) {
	now := time.Now()
	quotaErr := &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests}
	auth := &Auth{ID: "auth-level-quota"}

	for i := 1; i <= quota429CooldownThreshold-1; i++ {
		applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Duration(i)*time.Millisecond), false)
		if auth.Quota.BackoffLevel != i {
			t.Fatalf("failure %d BackoffLevel = %d, want %d", i, auth.Quota.BackoffLevel, i)
		}
		if !auth.NextRetryAfter.IsZero() || !auth.Quota.NextRecoverAt.IsZero() {
			t.Fatalf("failure %d cooled down early: next=%v quota=%v", i, auth.NextRetryAfter, auth.Quota.NextRecoverAt)
		}
		if auth.Unavailable || auth.Quota.Exceeded {
			t.Fatalf("failure %d marked auth unavailable before threshold: unavailable=%v quota=%+v", i, auth.Unavailable, auth.Quota)
		}
	}

	applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Second), false)
	if auth.Quota.BackoffLevel < quota429CooldownThreshold {
		t.Fatalf("eleventh BackoffLevel = %d, want >= %d", auth.Quota.BackoffLevel, quota429CooldownThreshold)
	}
	if !auth.NextRetryAfter.After(now) || !auth.Quota.NextRecoverAt.After(now) {
		t.Fatalf("eleventh did not cool down: next=%v quota=%v", auth.NextRetryAfter, auth.Quota.NextRecoverAt)
	}

	clearAuthStateOnSuccess(auth, now.Add(2*time.Second))
	if auth.Quota.BackoffLevel != 0 || !auth.NextRetryAfter.IsZero() || !auth.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("success did not clear auth-level 429 state: %+v", auth.Quota)
	}
}

func TestMarkResultQuota429RequiresConsecutiveFailures(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-quota-consecutive", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	model := "gpt-5-consecutive"
	serverErr := Result{
		AuthID:   auth.ID,
		Provider: "codex",
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "server_error", Message: "server", HTTPStatus: http.StatusInternalServerError},
	}

	for i := 0; i < 5; i++ {
		manager.MarkResult(context.Background(), quotaResult(auth.ID, model))
	}
	manager.MarkResult(context.Background(), serverErr)
	for i := 0; i < 6; i++ {
		manager.MarkResult(context.Background(), quotaResult(auth.ID, model))
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated.ModelStates[model] == nil {
		t.Fatalf("missing model state")
	}
	state := updated.ModelStates[model]
	if state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 6 {
		t.Fatalf("non-consecutive 429 cooled down or counted incorrectly: %+v", state.Quota)
	}

	for i := 0; i < quota429CooldownThreshold-6; i++ {
		manager.MarkResult(context.Background(), quotaResult(auth.ID, model))
	}
	cooled, ok := manager.GetByID(auth.ID)
	if !ok || cooled.ModelStates[model] == nil {
		t.Fatalf("missing cooled model state")
	}
	state = cooled.ModelStates[model]
	if !state.Quota.Exceeded || state.Quota.BackoffLevel < quota429CooldownThreshold || !state.Quota.NextRecoverAt.After(time.Now()) {
		t.Fatalf("eleven consecutive 429 did not cool down: %+v", state.Quota)
	}
	cooldownUntil := state.Quota.NextRecoverAt

	manager.MarkResult(context.Background(), serverErr)
	stillCooled, ok := manager.GetByID(auth.ID)
	if !ok || stillCooled.ModelStates[model] == nil {
		t.Fatalf("missing model state after concurrent 500")
	}
	state = stillCooled.ModelStates[model]
	if !state.Quota.Exceeded || !state.Quota.NextRecoverAt.Equal(cooldownUntil) {
		t.Fatalf("concurrent 500 cleared active 429 cooldown: %+v want recover %v", state.Quota, cooldownUntil)
	}

	manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: model, Success: true})
	for i := 0; i < quota429CooldownThreshold-1; i++ {
		manager.MarkResult(context.Background(), quotaResult(auth.ID, model))
	}
	restarted, ok := manager.GetByID(auth.ID)
	if !ok || restarted.ModelStates[model] == nil {
		t.Fatalf("missing restarted model state")
	}
	state = restarted.ModelStates[model]
	if state.Quota.Exceeded || state.Quota.BackoffLevel != quota429CooldownThreshold-1 {
		t.Fatalf("success did not restart 429 count from zero: %+v", state.Quota)
	}
	manager.MarkResult(context.Background(), quotaResult(auth.ID, model))
	recooled, ok := manager.GetByID(auth.ID)
	if !ok || recooled.ModelStates[model] == nil {
		t.Fatalf("missing recooled model state")
	}
	state = recooled.ModelStates[model]
	if !state.Quota.Exceeded {
		t.Fatalf("eleventh 429 after success did not cool down: %+v", state.Quota)
	}
}

func TestApplyAuthFailureStateQuota429RequiresConsecutiveFailures(t *testing.T) {
	now := time.Now()
	quotaErr := &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests}
	serverErr := &Error{Code: "server_error", Message: "server", HTTPStatus: http.StatusInternalServerError}
	auth := &Auth{ID: "auth-level-consecutive"}

	for i := 0; i < 5; i++ {
		applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Duration(i)*time.Millisecond), false)
	}
	applyAuthFailureState(auth, serverErr, nil, now.Add(10*time.Millisecond), false)
	for i := 0; i < 6; i++ {
		applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Duration(20+i)*time.Millisecond), false)
	}
	if auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.IsZero() || auth.Quota.BackoffLevel != 6 {
		t.Fatalf("non-consecutive auth 429 cooled down or counted incorrectly: %+v", auth.Quota)
	}

	for i := 0; i < quota429CooldownThreshold-6; i++ {
		applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Duration(40+i)*time.Millisecond), false)
	}
	if !auth.Quota.Exceeded || auth.Quota.BackoffLevel < quota429CooldownThreshold || !auth.Quota.NextRecoverAt.After(now) {
		t.Fatalf("auth eleven consecutive 429 did not cool down: %+v", auth.Quota)
	}
	cooldownUntil := auth.Quota.NextRecoverAt

	applyAuthFailureState(auth, serverErr, nil, now.Add(time.Second), false)
	if !auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.Equal(cooldownUntil) {
		t.Fatalf("auth concurrent 500 cleared active 429 cooldown: %+v want recover %v", auth.Quota, cooldownUntil)
	}

	clearAuthStateOnSuccess(auth, now.Add(2*time.Second))
	for i := 0; i < quota429CooldownThreshold-1; i++ {
		applyAuthFailureState(auth, quotaErr, nil, now.Add(time.Duration(3+i)*time.Second), false)
	}
	if auth.Quota.Exceeded || auth.Quota.BackoffLevel != quota429CooldownThreshold-1 {
		t.Fatalf("auth success did not restart 429 count from zero: %+v", auth.Quota)
	}
	applyAuthFailureState(auth, quotaErr, nil, now.Add(20*time.Second), false)
	if !auth.Quota.Exceeded {
		t.Fatalf("auth eleventh 429 after success did not cool down: %+v", auth.Quota)
	}
}

func TestJitteredCooldownWaitBounds(t *testing.T) {
	cases := []struct {
		wait      time.Duration
		maxWait   time.Duration
		maxJitter time.Duration
	}{
		{time.Second, 0, 250 * time.Millisecond},
		{8 * time.Second, 0, 2 * time.Second},
		{30 * time.Second, 0, 2 * time.Second},
		{time.Second, 30 * time.Second, 250 * time.Millisecond},
		{29 * time.Second, 30 * time.Second, time.Second},
	}
	for _, tc := range cases {
		for i := 0; i < 200; i++ {
			got := jitteredCooldownWait(tc.wait, tc.maxWait)
			if got < tc.wait || got >= tc.wait+tc.maxJitter {
				t.Fatalf("jitteredCooldownWait(%v, %v) = %v, want in [%v, %v)", tc.wait, tc.maxWait, got, tc.wait, tc.wait+tc.maxJitter)
			}
			if tc.maxWait > 0 && got > tc.maxWait {
				t.Fatalf("jitteredCooldownWait(%v, %v) = %v exceeds maxWait", tc.wait, tc.maxWait, got)
			}
		}
	}

	// maxWait is a hard ceiling: zero headroom disables jitter entirely.
	for i := 0; i < 50; i++ {
		if got := jitteredCooldownWait(30*time.Second, 30*time.Second); got != 30*time.Second {
			t.Fatalf("expected wait at maxWait to stay unjittered, got %v", got)
		}
	}

	if got := jitteredCooldownWait(0, time.Minute); got != 0 {
		t.Fatalf("expected zero wait to stay zero, got %v", got)
	}
	if got := jitteredCooldownWait(-time.Second, time.Minute); got != -time.Second {
		t.Fatalf("expected negative wait to pass through, got %v", got)
	}
	if got := jitteredCooldownWait(3, 0); got != 3 {
		t.Fatalf("expected sub-4ns wait to stay unchanged, got %v", got)
	}
}
