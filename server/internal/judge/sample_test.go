package judge

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestShouldSampleBoundaryRates(t *testing.T) {
	id := uuid.NewString()
	if ShouldSample(id, 0) {
		t.Fatalf("rate=0 must never sample")
	}
	if !ShouldSample(id, 1) {
		t.Fatalf("rate=1 must always sample")
	}
	if ShouldSample(id, -0.5) {
		t.Fatalf("negative rate must never sample")
	}
	if !ShouldSample(id, 1.5) {
		t.Fatalf("rate above 1 must always sample")
	}
}

func TestShouldSampleIsDeterministic(t *testing.T) {
	id := uuid.NewString()
	first := ShouldSample(id, 0.37)
	for i := 0; i < 100; i++ {
		if ShouldSample(id, 0.37) != first {
			t.Fatalf("ShouldSample must be deterministic for the same task id and rate")
		}
	}
}

func TestShouldSampleConvergesToRate(t *testing.T) {
	const n = 20000
	const rate = 0.1
	sampled := 0
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("task-%d", i)
		if ShouldSample(id, rate) {
			sampled++
		}
	}
	frac := float64(sampled) / float64(n)
	if frac < 0.08 || frac > 0.12 {
		t.Fatalf("expected sampled fraction near %.2f over %d ids, got %.4f (%d sampled)", rate, n, frac, sampled)
	}
}
