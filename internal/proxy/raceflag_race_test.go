//go:build race

package proxy

// raceAllocSlack is the allocation budget TestPooledParseReusesBuffers allows
// on top of the expected zero. The race detector's instrumentation shows up as
// allocations, so the test relaxes its check under -race.
const raceAllocSlack = 2.0
