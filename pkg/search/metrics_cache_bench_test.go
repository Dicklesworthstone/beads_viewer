package search

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/Dicklesworthstone/beads_viewer/pkg/testutil"
)

// Exercise the real immutable analyzer loader. The constant-hash loader used
// by the other cache microbenchmarks intentionally omits source hashing cost.
func BenchmarkAnalyzerMetricsCacheGet(b *testing.B) {
	for _, size := range []int{1000, 5000, 10000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			issues, err := testutil.PerformanceIssues("realistic", size, 20260904)
			if err != nil {
				b.Fatal(err)
			}
			cache := NewMetricsCache(NewAnalyzerMetricsLoader(issues))
			if err := cache.Refresh(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if metric, ok := cache.Get(issues[i%len(issues)].ID); !ok || metric.IssueID != issues[i%len(issues)].ID {
					b.Fatal("real cache lookup lost its issue")
				}
			}
		})
	}
}

func BenchmarkMetricsCacheGet(b *testing.B) {
	cache := buildBenchmarkMetricsCache(b, 1000)
	ids := buildBenchmarkIssueIDs(1000)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cache.Get(ids[i%len(ids)])
	}
}

func BenchmarkMetricsCacheGetBatch(b *testing.B) {
	cache := buildBenchmarkMetricsCache(b, 1000)
	ids := buildBenchmarkIssueIDs(100)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.GetBatch(ids)
	}
}

func BenchmarkMetricsCacheMemory(b *testing.B) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	before := m.Alloc

	loader := &benchmarkMetricsLoader{
		metrics:  buildBenchmarkMetrics(10000),
		dataHash: "bench-10000",
	}
	cache := NewMetricsCache(loader)
	if err := cache.Refresh(); err != nil {
		b.Fatalf("Refresh metrics cache: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&m)
	after := m.Alloc

	_, _ = cache.Get("issue-0")
	b.ReportMetric(float64(after-before)/1024.0, "KB")
}
