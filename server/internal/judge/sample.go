package judge

import "hash/fnv"

// ShouldSample decides whether taskID falls inside the configured
// sample rate. It hashes the task id rather than calling math/rand so
// the decision is deterministic and stable across retries/restarts of
// the sampler job: the same task always lands on the same side of the
// line for a given rate, instead of a fresh coin flip on every attempt
// (which would let a flaky retry eventually score a task that was
// never meant to be sampled).
//
// rate is clamped to [0, 1]; rate<=0 never samples, rate>=1 always
// does.
func ShouldSample(taskID string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(taskID))
	// Map the 32-bit hash into [0, 1) and compare against rate so the
	// fraction of sampled task ids converges to `rate` for any
	// reasonably-distributed set of UUIDs.
	frac := float64(h.Sum32()) / float64(1<<32)
	return frac < rate
}
