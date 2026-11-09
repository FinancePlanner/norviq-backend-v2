//go:build race

package attachment

// raceSlack scales timing thresholds: the race detector slows code about 10x.
const raceSlack = 8
