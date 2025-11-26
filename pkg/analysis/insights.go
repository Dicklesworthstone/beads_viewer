package analysis

import (
	"sort"
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
	Orphans        []string      // No dependencies (and not blocked?) - Leaf nodes
	Cycles         [][]string
	ClusterDensity float64

	// Full stats for calculation explanations
	Stats *GraphStats
}

// GenerateInsights translates raw stats into actionable data
func (s GraphStats) GenerateInsights(limit int) Insights {
	return Insights{
		Bottlenecks: getTopItems(s.Betweenness, limit),
		Keystones:   getTopItems(s.CriticalPathScore, limit),
		Influencers: getTopItems(s.Eigenvector, limit),
		Hubs:        getTopItems(s.Hubs, limit),
		Authorities: getTopItems(s.Authorities, limit),
		Cycles:      s.Cycles,
		ClusterDensity: s.Density,
		Stats:       &s,
	}
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
		return ss[i].Value > ss[j].Value
	})

	var result []InsightItem
	for i := 0; i < len(ss) && i < limit; i++ {
		result = append(result, InsightItem{ID: ss[i].Key, Value: ss[i].Value})
	}
	return result
}
