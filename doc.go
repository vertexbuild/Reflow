// Package reflow builds concurrent, streaming workflow graphs for Go
// with structured handoffs.
//
// Every node follows three phases: Resolve inspects the incoming
// envelope, Act does the work, and Settle prepares a handoff for
// whatever comes next. The envelope carries the current value plus
// structured context — hints, trace steps, and tags — that
// accumulates as it moves through the graph.
//
// Compose nodes with [Chain], [ForkJoin], [Pipeline], [Pool],
// [Split], [Merge], and [Compose]. Stream them with [Stream] and
// [Collect]. Wrap external calls with [Tool], [Use], and [Invoke]
// for automatic tracing. Add feedback loops with [WithRetry].
//
// The core module has zero external dependencies.
package reflow
