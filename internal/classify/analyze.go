package classify

import "context"

// Analyze is the full single-request entry point: Extract → Classify → (Fill).
//
//   - A tool_continuation / subagent round returns immediately with just its Role
//     set; it is never classified or sent to a model (it inherits its parent turn).
//   - When rules fully decide (NeedHaiku == false) or hc == nil (pure-rules mode),
//     the rule verdict is returned as-is.
//   - Otherwise Haiku fills the gaps. If Fill fails, the rule verdict is returned
//     alongside the error so the caller can persist the partial result and retry
//     (FR-010) — the error is non-fatal to the verdict.
func Analyze(ctx context.Context, req Request, cfg Config, hc *HaikuClient) (Result, error) {
	role := RequestRole(req)
	if role != RoleUserInitiated {
		return Result{Role: role}, nil
	}

	sig := Extract(req, cfg)
	res := Classify(req, sig, cfg)
	if !res.NeedHaiku || hc == nil {
		return res, nil
	}

	if err := hc.Fill(ctx, sig, &res); err != nil {
		// Degrade gracefully: keep the rule-only verdict, surface the error.
		return res, err
	}
	return res, nil
}

// AnalyzeSignal is the analyzer-side entry point used by cmd/check, which already
// holds a persisted Signal (the raw request is long gone). It mirrors Analyze but
// skips Extract. Role is supplied by the caller (predetermined at collection).
func AnalyzeSignal(ctx context.Context, sig Signal, role Role, cfg Config, hc *HaikuClient) (Result, error) {
	if role != RoleUserInitiated {
		return Result{Role: role}, nil
	}
	res := classifyFromSignal(sig, role, cfg)
	if !res.NeedHaiku || hc == nil {
		return res, nil
	}
	if err := hc.Fill(ctx, sig, &res); err != nil {
		return res, err
	}
	return res, nil
}
