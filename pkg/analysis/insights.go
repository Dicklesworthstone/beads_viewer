package analysis

import (
	"sort"
	"time"
)

// InsightItem represents a single item in an insight list with its metric value
type InsightItem struct {
	ID    string
	Value float64
}

// Insights is a high-level summary of graph analysis
type Insights struct {
	Bottlenecks    []InsightItem // Top betweenness nodes
	Keystones      []InsightItem // Top impact nodes
	Influencers    []InsightItem // Top eigenvector centrality
	Hubs           []InsightItem // Strong dependency aggregators
	Authorities    []InsightItem // Strong prerequisite providers
	Cores          []InsightItem // Highest k-core numbers (structural cohesion)
	Articulation   []string      // Cut vertices whose removal disconnects graph
	Slack          []InsightItem // Highest slack (parallelizable / flexible nodes)
	Orphans        []string      // No dependencies (and not blocked?) - Leaf nodes
	Cycles         [][]string
	ClusterDensity float64
	Velocity       *VelocitySnapshot

	// Full stats for calculation explanations
	Stats *GraphStats
}

// VelocitySnapshot is a lightweight view of project throughput for insights.
type VelocitySnapshot struct {
	Closed7    int         `json:"closed_last_7_days"`
	Closed30   int         `json:"closed_last_30_days"`
	AvgDays    float64     `json:"avg_days_to_close"`
	Weekly     []int       `json:"weekly,omitempty"` // counts, newest first
	Estimated  bool        `json:"estimated,omitempty"`
	WeekStarts []time.Time `json:"week_starts,omitempty"`
}

// GenerateInsights translates raw stats into actionable data
func (s *GraphStats) GenerateInsights(limit int) Insights {
	// Get thread-safe copies of all Phase 2 data
	pageRank := s.PageRank()
	betweenness := s.Betweenness()
	criticalPath := s.CriticalPathScore()
	eigenvector := s.Eigenvector()
	hubs := s.Hubs()
	authorities := s.Authorities()
	coreNum := s.CoreNumber()
	artPts := s.ArticulationPoints()
	slack := s.Slack()
	cycles := s.Cycles()
	if cycles == nil {
		cycles = make([][]string, 0)
	}
	orphans := findOrphans(s.OutDegree)

	// Velocity snapshot (populated later when triage provides it)
	var velocity *VelocitySnapshot

	if limit <= 0 {
		maxLen := 0
		metricLens := []int{
			len(pageRank),
			len(betweenness),
			len(criticalPath),
			len(eigenvector),
			len(hubs),
			len(authorities),
			len(coreNum),
			len(slack),
		}
		for _, l := range metricLens {
			if l > maxLen {
				maxLen = l
			}
		}
		if maxLen > 0 {
			limit = maxLen
		} else {
			limit = s.NodeCount
		}
	}

	return Insights{
		Bottlenecks:    getTopItems(betweenness, limit),
		Keystones:      getTopItems(criticalPath, limit),
		Influencers:    getTopItems(eigenvector, limit),
		Hubs:           getTopItems(hubs, limit),
		Authorities:    getTopItems(authorities, limit),
		Cores:          getTopItemsInt(coreNum, limit),
		Articulation:   limitStrings(artPts, limit),
		Slack:          getTopItems(slack, limit),
		Orphans:        limitStrings(orphans, limit),
		Cycles:         cycles,
		ClusterDensity: s.Density,
		Velocity:       velocity,
		Stats:          s,
	}
}

func findOrphans(outDegree map[string]int) []string {
	ids := make([]string, 0)
	for id, deg := range outDegree {
		if deg == 0 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func getTopItems(m map[string]float64, limit int) []InsightItem {
	type kv struct {
		Key   string
		Value float64
	}
	var ss []kv
	for k, v := range m {
		ss = append(ss, kv{k, v})
	}

	sort.Slice(ss, func(i, j int) bool {
		if ss[i].Value == ss[j].Value {
			return ss[i].Key < ss[j].Key // deterministic tie-break
		}
		return ss[i].Value > ss[j].Value
	})

	result := make([]InsightItem, 0)
	for i := 0; i < len(ss) && i < limit; i++ {
		result = append(result, InsightItem{ID: ss[i].Key, Value: ss[i].Value})
	}
	return result
}

func getTopItemsInt(m map[string]int, limit int) []InsightItem {
	type kv struct {
		Key   string
		Value int
	}
	var ss []kv
	for k, v := range m {
		ss = append(ss, kv{k, v})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].Value == ss[j].Value {
			return ss[i].Key < ss[j].Key // deterministic tie-break
		}
		return ss[i].Value > ss[j].Value
	})
	result := make([]InsightItem, 0)
	for i := 0; i < len(ss) && i < limit; i++ {
		result = append(result, InsightItem{ID: ss[i].Key, Value: float64(ss[i].Value)})
	}
	return result
}

func limitStrings(s []string, limit int) []string {
	if s == nil {
		return []string{}
	}
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit]
}
