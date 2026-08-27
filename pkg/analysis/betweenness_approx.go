package analysis

import (
	"math/rand"
	"runtime"
	"sort"
	"sync"
	"time"

	"gonum.org/v1/gonum/graph"
	"gonum.org/v1/gonum/graph/network"
)

type denseIndex struct {
	idToIdx map[int64]int
	idxToID []int64
}

func buildDenseIndex(nodes []graph.Node) denseIndex {
	idxToID := make([]int64, len(nodes))
	idToIdx := denseIndexMapPool.Get().(map[int64]int)
	clear(idToIdx)
	for i, n := range nodes {
		id := n.ID()
		idxToID[i] = id
		idToIdx[id] = i
	}
	return denseIndex{
		idToIdx: idToIdx,
		idxToID: idxToID,
	}
}

type cachedAdjacency struct {
	outgoing [][]int
	incoming [][]int
}

func buildCachedAdjacency(g graph.Directed, idx denseIndex) cachedAdjacency {
	nodeCount := len(idx.idxToID)
	outgoing := make([][]int, nodeCount)
	incoming := make([][]int, nodeCount)

	for vIdx, vID := range idx.idxToID {
		to := g.From(vID)
		capHint := to.Len()
		if capHint < 0 {
			capHint = 0
		}
		neighbors := make([]int, 0, capHint)
		for to.Next() {
			wID := to.Node().ID()
			wIdx, ok := idx.idToIdx[wID]
			if !ok {
				continue
			}
			neighbors = append(neighbors, wIdx)
		}
		sort.Ints(neighbors)
		outgoing[vIdx] = neighbors
	}

	// Build incoming adjacency from the already-built outgoing adjacency.
	for vIdx, neighbors := range outgoing {
		for _, wIdx := range neighbors {
			incoming[wIdx] = append(incoming[wIdx], vIdx)
		}
	}
	for i := range incoming {
		sort.Ints(incoming[i])
	}

	return cachedAdjacency{
		outgoing: outgoing,
		incoming: incoming,
	}
}

// brandesBuffers holds reusable data structures for Brandes' algorithm using dense indexing.
// These buffers are pooled via sync.Pool to avoid per-call allocations.
//
// Memory characteristics (n = number of nodes):
//   - sigma: stores shortest path counts, O(n)
//   - dist: stores BFS distances (-1 = unvisited), O(n)
//   - delta: stores dependency accumulation, O(n)
//   - pred: stores predecessor lists as dense indices, O(n) slices + O(E) total capacity
//   - queue: BFS frontier, up to O(n) capacity
//   - stack: reverse order for accumulation, up to O(n) capacity
//   - neighbors: temporary slice for iterator results, typically small
//   - bc: per-source betweenness contributions, O(n)
type brandesBuffers struct {
	sigma     []float64 // σ_s(v)
	dist      []int     // d_s(v) (-1 = infinity/unvisited)
	delta     []float64 // δ_s(v)
	pred      [][]int   // P_s(v) = predecessors as dense indices
	queue     []int     // BFS queue (FIFO)
	stack     []int     // Visited nodes in BFS order (LIFO for backprop)
	neighbors []int     // Temp slice to collect neighbor indices from iterator
	bc        []float64 // Per-source betweenness contributions
}

// brandesPool provides reusable buffer sets for singleSourceBetweennessDense.
// Pre-allocation with capacity 256 handles most real-world graphs efficiently;
// slices will grow if needed but retain capacity for subsequent reuse.
//
// Concurrency: sync.Pool is safe for concurrent Get/Put. Each goroutine
// gets its own buffer; no synchronization needed during algorithm execution.
//
// GC behavior: Pool may discard buffers during GC. This is acceptable since
// New() will create fresh buffers as needed; we trade occasional allocations
// for reduced peak memory during steady-state operation.
var brandesPool = sync.Pool{
	New: func() interface{} {
		return &brandesBuffers{
			sigma:     make([]float64, 0, 256),
			dist:      make([]int, 0, 256),
			delta:     make([]float64, 0, 256),
			pred:      make([][]int, 0, 256),
			queue:     make([]int, 0, 256),
			stack:     make([]int, 0, 256),
			neighbors: make([]int, 0, 32),
			bc:        make([]float64, 0, 256),
		}
	},
}

var approxNodesPool = sync.Pool{
	New: func() interface{} {
		return make([]graph.Node, 0, 256)
	},
}

var denseIndexMapPool = sync.Pool{
	New: func() interface{} {
		return make(map[int64]int, 256)
	},
}

func pooledNodesOf(it graph.Nodes) []graph.Node {
	nodes := approxNodesPool.Get().([]graph.Node)
	nodes = nodes[:0]
	for it.Next() {
		nodes = append(nodes, it.Node())
	}
	return nodes
}

func putPooledNodes(nodes []graph.Node) {
	// Avoid retaining extremely large backing arrays in the pool.
	const maxCap = 50_000
	if cap(nodes) > maxCap {
		return
	}
	approxNodesPool.Put(nodes[:0])
}

// reset clears buffer contents while retaining allocated capacity.
// Must be called before each new source node BFS traversal.
//
// Memory strategy:
//   - If slices grew >2x node count, reallocate to allow GC of oversized backing arrays
//   - Slices reset via [:0] to retain backing array
//
// Initialization values match fresh-allocation semantics:
//   - sigma[i] = 0 (no paths counted yet)
//   - dist[i] = -1 (infinity/unvisited sentinel)
//   - delta[i] = 0 (no dependency accumulated)
//   - pred[i] = pred[i][:0] (empty predecessor list, retain slice capacity)
func (b *brandesBuffers) reset(nodeCount int) {
	// Resize backing arrays when the graph size changes significantly:
	// - Grow when capacity is insufficient
	// - Shrink when previous capacity is >2x node count (avoid unbounded retention)
	if cap(b.sigma) < nodeCount || cap(b.sigma) > nodeCount*2 {
		b.sigma = make([]float64, 0, nodeCount)
		b.dist = make([]int, 0, nodeCount)
		b.delta = make([]float64, 0, nodeCount)
		b.pred = make([][]int, 0, nodeCount)
		b.queue = make([]int, 0, nodeCount)
		b.stack = make([]int, 0, nodeCount)
		b.bc = make([]float64, 0, nodeCount)
	}

	b.sigma = b.sigma[:nodeCount]
	clear(b.sigma)

	b.dist = b.dist[:nodeCount]
	for i := range b.dist {
		b.dist[i] = -1
	}

	b.delta = b.delta[:nodeCount]
	clear(b.delta)

	b.bc = b.bc[:nodeCount]
	clear(b.bc)

	// Reset predecessor lists while retaining per-node slice capacity.
	if cap(b.pred) < nodeCount {
		b.pred = make([][]int, nodeCount)
	} else {
		b.pred = b.pred[:nodeCount]
	}
	for i := range b.pred {
		if b.pred[i] != nil {
			b.pred[i] = b.pred[i][:0]
			continue
		}
		b.pred[i] = make([]int, 0, 4)
	}

	// Reset auxiliary slices (retain capacity)
	b.queue = b.queue[:0]
	b.stack = b.stack[:0]
	b.neighbors = b.neighbors[:0]
}

// BetweennessMode specifies how betweenness centrality should be computed.
type BetweennessMode string

const (
	// BetweennessExact computes exact betweenness centrality using Brandes' algorithm.
	// Complexity: O(V*E) - fast for small graphs, slow for large graphs.
	BetweennessExact BetweennessMode = "exact"

	// BetweennessApproximate uses sampling-based approximation.
	// Complexity: O(k*E) where k is the sample size - much faster for large graphs.
	// Error: O(1/sqrt(k)) - with k=100, ~10% error in ranking.
	BetweennessApproximate BetweennessMode = "approximate"

	// BetweennessSkip disables betweenness computation entirely.
	BetweennessSkip BetweennessMode = "skip"
)

// BetweennessResult contains the result of betweenness computation.
type BetweennessResult struct {
	// Scores maps node IDs to their betweenness centrality scores
	Scores map[int64]float64

	// Mode indicates how the result was computed
	Mode BetweennessMode

	// SampleSize is the number of pivot nodes used (only for approximate mode)
	SampleSize int

	// TotalNodes is the total number of nodes in the graph
	TotalNodes int

	// Elapsed is the time taken to compute
	Elapsed time.Duration

	// TimedOut indicates if computation was interrupted by timeout
	TimedOut bool
}

// ApproxBetweenness computes approximate betweenness centrality using sampling.
//
// Instead of computing shortest paths from ALL nodes (O(V*E)), we sample k pivot
// nodes and extrapolate. This is Brandes' approximation algorithm.
//
// Error bounds: With k samples, approximation error is O(1/sqrt(k)):
//   - k=50: ~14% error
//   - k=100: ~10% error
//   - k=200: ~7% error
//
// For ranking purposes (which node is most central), this is usually sufficient.
//
// References:
//   - "A Faster Algorithm for Betweenness Centrality" (Brandes, 2001)
//   - "Approximating Betweenness Centrality" (Bader et al., 2007)
func ApproxBetweenness(g graph.Directed, sampleSize int, seed int64) BetweennessResult {
	return approxBetweenness(g, sampleSize, seed, false)
}

// approxBetweennessDeterministic performs the same seeded sampling while
// reducing pivot contributions in sample order. The ordinary implementation
// keeps its parallel fast path; reproducible robot mode uses this serial
// reduction because floating-point addition in goroutine completion order is
// not associative and can otherwise change output bytes between runs.
func approxBetweennessDeterministic(g graph.Directed, sampleSize int, seed int64) BetweennessResult {
	return approxBetweenness(g, sampleSize, seed, true)
}

func approxBetweenness(g graph.Directed, sampleSize int, seed int64, deterministic bool) BetweennessResult {
	start := time.Now()
	nodes := pooledNodesOf(g.Nodes())
	defer putPooledNodes(nodes)
	n := len(nodes)
	// Ensure deterministic ordering before sampling; gonum's Nodes may be map-backed.
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID() < nodes[j].ID() })

	// Clamp sampleSize to valid range [1, n] to prevent division by zero and negative slice indices
	if sampleSize < 1 {
		sampleSize = 1
	}

	result := BetweennessResult{
		Scores:     make(map[int64]float64),
		Mode:       BetweennessApproximate,
		SampleSize: sampleSize,
		TotalNodes: n,
	}

	if n == 0 {
		// SampleSize reports pivots actually used, not the caller's requested
		// size. An empty graph has no valid pivot even though non-empty graphs
		// clamp a non-positive request to one.
		result.SampleSize = 0
		result.Elapsed = time.Since(start)
		return result
	}

	// For small graphs or when sample size >= node count, use exact algorithm
	if sampleSize >= n {
		exact := network.Betweenness(g)
		result.Scores = exact
		result.Mode = BetweennessExact
		result.SampleSize = n
		result.Elapsed = time.Since(start)
		return result
	}

	idx := buildDenseIndex(nodes)
	adj := buildCachedAdjacency(g, idx)
	// idx.idToIdx is only needed for adjacency construction; return it to the pool early.
	if idx.idToIdx != nil {
		denseIndexMapPool.Put(idx.idToIdx)
		idx.idToIdx = nil
	}

	// Sample k random pivot indices
	pivots := sampleIndices(n, sampleSize, seed)

	// Compute partial betweenness from sampled pivots.
	partialBC := make([]float64, n)
	if deterministic {
		buf := brandesPool.Get().(*brandesBuffers)
		for _, pivot := range pivots {
			singleSourceBetweennessDense(adj, pivot, buf)
			for _, w := range buf.stack {
				partialBC[w] += buf.bc[w]
			}
		}
		brandesPool.Put(buf)
	} else {
		var mu sync.Mutex
		var wg sync.WaitGroup

		// Limit concurrency to avoid excessive goroutines
		sem := make(chan struct{}, runtime.NumCPU())

		for _, pivot := range pivots {
			wg.Add(1)
			go func(sourceIdx int) {
				defer wg.Done()
				sem <- struct{}{} // Acquire token
				defer func() { <-sem }()

				buf := brandesPool.Get().(*brandesBuffers)
				defer brandesPool.Put(buf)

				// Compute local contribution into pooled buffers (buf.bc)
				singleSourceBetweennessDense(adj, sourceIdx, buf)

				// Merge into global result using visited nodes only.
				mu.Lock()
				for _, w := range buf.stack {
					partialBC[w] += buf.bc[w]
				}
				mu.Unlock()
			}(pivot)
		}
		wg.Wait()
	}

	// Scale up: BC_approx = BC_partial * (n / k)
	// This extrapolates from the sample to the full graph
	scale := float64(n) / float64(sampleSize)
	scores := make(map[int64]float64, n)
	for i, val := range partialBC {
		if val == 0 {
			continue
		}
		scores[idx.idxToID[i]] = val * scale
	}
	result.Scores = scores
	result.Elapsed = time.Since(start)
	return result
}

// sampleIndices returns a random sample of k indices from [0,n).
// Uses Fisher-Yates shuffle for unbiased sampling.
func sampleIndices(n, k int, seed int64) []int {
	if k >= n {
		idxs := make([]int, n)
		for i := range idxs {
			idxs[i] = i
		}
		return idxs
	}

	// Create a copy to avoid modifying the original
	shuffled := make([]int, n)
	for i := range shuffled {
		shuffled[i] = i
	}

	// Fisher-Yates shuffle for first k elements
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < k; i++ {
		j := i + rng.Intn(len(shuffled)-i)
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}

	return shuffled[:k]
}

// singleSourceBetweennessDense computes the betweenness contribution from a single source index.
// This is the core of Brandes' algorithm, run once per pivot, using dense indexing.
//
// The algorithm performs BFS from the source and accumulates dependency scores
// in a reverse topological order traversal.
func singleSourceBetweennessDense(adj cachedAdjacency, sourceIdx int, buf *brandesBuffers) {
	nodeCount := len(adj.outgoing)
	if nodeCount == 0 {
		return
	}

	// Initialize buffer for this source (clears previous state while retaining capacity).
	buf.reset(nodeCount)

	// Use pooled data structures (aliases for readability)
	sigma := buf.sigma
	dist := buf.dist
	delta := buf.delta
	pred := buf.pred

	sigma[sourceIdx] = 1
	dist[sourceIdx] = 0

	// Queue for BFS (reuse pooled slice)
	buf.queue = append(buf.queue, sourceIdx)

	// BFS phase
	head := 0
	for head < len(buf.queue) {
		v := buf.queue[head]
		head++
		buf.stack = append(buf.stack, v)

		for _, w := range adj.outgoing[v] {
			// Path discovery
			if dist[w] < 0 {
				dist[w] = dist[v] + 1
				buf.queue = append(buf.queue, w)
			}

			// Path counting
			if dist[w] == dist[v]+1 {
				sigma[w] += sigma[v]
				pred[w] = append(pred[w], v)
			}
		}
	}

	// Accumulation phase
	for i := len(buf.stack) - 1; i >= 0; i-- {
		w := buf.stack[i]
		if w == sourceIdx {
			continue
		}

		for _, v := range pred[w] {
			if sigma[w] > 0 {
				delta[v] += (sigma[v] / sigma[w]) * (1 + delta[w])
			}
		}

		// Add dependency to betweenness (w != sourceIdx already checked above)
		buf.bc[w] += delta[w]
	}
}

// RecommendSampleSize returns a recommended sample size based on graph characteristics.
// The goal is to balance accuracy vs. speed.
//
// Note: edgeCount is accepted for future density-aware heuristics but currently unused.
func RecommendSampleSize(nodeCount, edgeCount int) int {
	_ = edgeCount // Reserved for future density-aware sampling heuristics
	if nodeCount <= 0 {
		return 0
	}
	switch {
	case nodeCount < 100:
		// Small graph: use exact algorithm
		return nodeCount
	case nodeCount < 500:
		// Medium graph: 20% sample for ~22% error
		minSample := 50
		sample := nodeCount / 5
		if sample > minSample {
			return sample
		}
		return minSample
	case nodeCount < 2000:
		// Large graph: fixed sample for ~10% error
		return 100
	default:
		// XL graph: larger fixed sample
		return 200
	}
}
