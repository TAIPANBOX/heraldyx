package render

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// @measured go test ./internal/render -run TestRunStalled 2026-09-19:
// run_stalled used fallback wording and omitted the last call and silence.
func TestRunStalledNamesTheObservationWithoutDiagnosingTheCause(t *testing.T) {
	m := Event(cfg(), ev("run_stalled", map[string]any{"last_call_millis": float64(1789822800000), "silence_ms": float64(300000)}), now, "", nil)
	for _, want := range []string{"went quiet", "2026-09-19T13:00:00Z", "300 seconds", "run-42", "cannot tell"} {
		if !strings.Contains(m.Subject+m.Body, want) {
			t.Errorf("missing %q: %s", want, m.Body)
		}
	}
	for _, bad := range []string{"node died", "provider failed", "run is stopped"} {
		if strings.Contains(m.Body, bad) {
			t.Errorf("invented cause %q", bad)
		}
	}
	t.Log(m.Body)
}

func TestRunStalledRejectsInvalidNumbersWithoutRenderingContent(t *testing.T) {
	bad := []any{nil, "private-prompt", true, -1, -0.5, math.NaN(), math.Inf(1), float64(1 << 63), 1.25}
	rng := rand.New(rand.NewSource(20260919))
	for i := 0; i < 1024; i++ {
		n := float64(rng.Intn(1000000))
		bad = append(bad, -n-1, n+0.5)
	}
	for _, value := range bad {
		m := Event(cfg(), ev("run_stalled", map[string]any{"last_call_millis": value, "silence_ms": value}), now, "", nil)
		if strings.Contains(m.Body, "private-prompt") || strings.Contains(m.Body, "Last call at") || strings.Contains(m.Body, "silent for") {
			t.Fatalf("invalid %v rendered: %s", value, m.Body)
		}
	}
}
