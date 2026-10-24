package leveling

import "sort"

// maxCurveLevel bounds the precomputed table. Configs allow 1..1000, so no
// valid max level can index past levelFloor.
const maxCurveLevel = 1000

// levelFloor[L] is the total XP needed to reach level L. The MEE6-style cost
// to go from L to L+1 is 5L² + 50L + 100.
var levelFloor [maxCurveLevel + 1]int64

func init() {
	for l := range maxCurveLevel {
		levelFloor[l+1] = levelFloor[l] + int64(5*l*l+50*l+100)
	}
}

// levelForXP resolves the level a member with xp has reached, the XP earned
// inside that level, and the XP required to reach the next one.
//
// At maxLevel the curve is exhausted: progress and next are both 0, which
// callers render as "MAX".
func levelForXP(xp int64, maxLevel int) (level int, progress, next int64) {
	if maxLevel < 1 {
		maxLevel = 1
	}
	if maxLevel > maxCurveLevel {
		maxLevel = maxCurveLevel
	}
	if xp < 0 {
		xp = 0
	}
	// Largest level in [0,maxLevel] whose floor is still affordable.
	level = sort.Search(maxLevel+1, func(i int) bool {
		return levelFloor[i] > xp
	}) - 1
	if level < 0 {
		level = 0
	}
	if level >= maxLevel {
		return maxLevel, 0, 0
	}
	return level, xp - levelFloor[level], levelFloor[level+1] - levelFloor[level]
}
