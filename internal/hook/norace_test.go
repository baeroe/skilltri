//go:build !race

package hook

// raceEnabled relaxes timing limits under the race detector.
const raceEnabled = false
