//go:build !race

package proxy

// raceAllocSlack is the allocation budget TestPooledParseReusesBuffers allows
// on top of the expected zero. Without the race detector the pooled path must
// allocate nothing at all.
const raceAllocSlack = 0.0
