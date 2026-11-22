package eval

import "time"

type Kind string

const (
	KindEpisodic Kind = "episodic"
	KindSemantic Kind = "semantic"
)

type QueryCategory string

const (
	CategoryDirect         QueryCategory = "direct"
	CategoryAssociative    QueryCategory = "associative"
	CategoryNicknameDrift  QueryCategory = "nickname_drift"
	CategoryTemporal       QueryCategory = "temporal"
	CategoryNoiseRejection QueryCategory = "noise_rejection"
)

type Stage string

const (
	StageB0 Stage = "B0"
	StageB1 Stage = "B1"
	StageB2 Stage = "B2"
	StageB3 Stage = "B3"
	StageB4 Stage = "B4"
)

type DecayModel string

const (
	DecayExponential DecayModel = "exponential"
	DecayPowerLaw    DecayModel = "power_law"
)

type Memory struct {
	ID           string
	Kind         Kind
	Text         string
	TEvent       int64
	TIngest      int64
	SupersededBy string
	// MergedSource marks episodic fragments consolidated into a semantic final fact.
	MergedSource bool
	// Consolidated marks semantic final facts distilled during Dreaming; excluded in B0/B1 raw baselines.
	Consolidated bool
	Importance   int
	Strength     float64
	LastRecalled int64
	RecallCount  int
	Archived     bool
	Entities     []string
	IsDistractor bool
}

type AssocEdge struct {
	A         string
	B         string
	Weight    float64
	LastFired int64
}

type RecallEvent struct {
	MemoryID   string
	Timestamp  int64
	BotReplied bool
	Relevant   bool
}

type Query struct {
	ID            string
	Category      QueryCategory
	Speaker       string
	Text          string
	Timestamp     int64
	ExpectedIDs   []string
	SupersededIDs []string
}

type Dataset struct {
	Now          int64
	Memories     []Memory
	Edges        []AssocEdge
	Aliases      map[string]string
	RecallEvents []RecallEvent
	Queries      []Query
}

type Hit struct {
	MemoryID string
	Score    float64
	Rank     int
}

type QueryOutcome struct {
	Query   Query
	Hits    []Hit
	Latency time.Duration
}

type CategoryMetrics struct {
	Count            int
	RecallAt4        float64
	MRR              float64
	TemporalAccuracy float64
	SupersededLeak   float64
	NoiseRate        float64
}

type Metrics struct {
	RecallAt4        float64
	MRR              float64
	TemporalAccuracy float64
	SupersededLeak   float64
	NoiseRate        float64
	DistractorRate   float64
	AvgReturned      float64
	LatencyP50       time.Duration
	LatencyP95       time.Duration
	LatencyP99       time.Duration
	ByCategory       map[QueryCategory]CategoryMetrics
}
