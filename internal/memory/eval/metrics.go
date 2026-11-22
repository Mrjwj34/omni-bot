package eval

import (
	"slices"
	"time"
)

func Evaluate(outcomes []QueryOutcome, topK int) Metrics {
	if topK <= 0 {
		topK = 4
	}

	var (
		posCount         int
		recallSum        float64
		mrrSum           float64
		tempCount        int
		tempCorrect      int
		tempLeak         int
		noiseCount       int
		noiseFP          int
		totalReturned    int
		totalDistractors int
	)

	latencies := make([]time.Duration, 0, len(outcomes))
	byCat := make(map[QueryCategory]*catAccum)

	for _, oc := range outcomes {
		latencies = append(latencies, oc.Latency)
		acc := byCat[oc.Query.Category]
		if acc == nil {
			acc = &catAccum{}
			byCat[oc.Query.Category] = acc
		}
		acc.count++

		hits := oc.Hits
		if len(hits) > topK {
			hits = hits[:topK]
		}
		totalReturned += len(hits)

		expected := toSet(oc.Query.ExpectedIDs)
		superseded := toSet(oc.Query.SupersededIDs)

		if len(expected) == 0 {
			noiseCount++
			if len(hits) > 0 {
				noiseFP++
				acc.noiseFP++
				totalDistractors += len(hits)
			}
			continue
		}

		posCount++
		matched := 0
		firstRelRank := 0
		for i, h := range hits {
			if expected[h.MemoryID] {
				matched++
				if firstRelRank == 0 {
					firstRelRank = i + 1
				}
			} else {
				totalDistractors++
			}
		}

		rec := float64(matched) / float64(len(expected))
		recallSum += rec
		acc.recallSum += rec

		if firstRelRank > 0 {
			rr := 1.0 / float64(firstRelRank)
			mrrSum += rr
			acc.mrrSum += rr
		}

		if oc.Query.Category == CategoryTemporal {
			tempCount++
			latestPos := -1
			supersededPos := -1
			for i, h := range hits {
				if latestPos < 0 && expected[h.MemoryID] {
					latestPos = i
				}
				if supersededPos < 0 && superseded[h.MemoryID] {
					supersededPos = i
				}
			}
			if supersededPos >= 0 {
				tempLeak++
				acc.tempLeak++
			}
			// Latest valid fact must surface at rank 1 and strictly beat any superseded state.
			if latestPos == 0 && (supersededPos < 0 || latestPos < supersededPos) {
				tempCorrect++
				acc.tempCorrect++
			}
		}
	}

	m := Metrics{
		LatencyP50: Percentile(latencies, 50),
		LatencyP95: Percentile(latencies, 95),
		LatencyP99: Percentile(latencies, 99),
		ByCategory: make(map[QueryCategory]CategoryMetrics, len(byCat)),
	}
	if posCount > 0 {
		m.RecallAt4 = recallSum / float64(posCount)
		m.MRR = mrrSum / float64(posCount)
	}
	if tempCount > 0 {
		m.TemporalAccuracy = float64(tempCorrect) / float64(tempCount)
		m.SupersededLeak = float64(tempLeak) / float64(tempCount)
	}
	if noiseCount > 0 {
		m.NoiseRate = float64(noiseFP) / float64(noiseCount)
	}
	if totalReturned > 0 {
		m.DistractorRate = float64(totalDistractors) / float64(totalReturned)
	}
	if len(outcomes) > 0 {
		m.AvgReturned = float64(totalReturned) / float64(len(outcomes))
	}

	for cat, acc := range byCat {
		cm := CategoryMetrics{Count: acc.count}
		if acc.count > 0 {
			if cat == CategoryNoiseRejection {
				cm.NoiseRate = float64(acc.noiseFP) / float64(acc.count)
			} else {
				cm.RecallAt4 = acc.recallSum / float64(acc.count)
				cm.MRR = acc.mrrSum / float64(acc.count)
			}
			if cat == CategoryTemporal {
				cm.TemporalAccuracy = float64(acc.tempCorrect) / float64(acc.count)
				cm.SupersededLeak = float64(acc.tempLeak) / float64(acc.count)
			}
		}
		m.ByCategory[cat] = cm
	}

	return m
}

type catAccum struct {
	count       int
	recallSum   float64
	mrrSum      float64
	tempCorrect int
	tempLeak    int
	noiseFP     int
}

// Percentile computes the nearest-rank percentile for p in [0, 100].
func Percentile(samples []time.Duration, p float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int((p/100.0)*float64(len(sorted)) + 0.999999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func toSet(ids []string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}
