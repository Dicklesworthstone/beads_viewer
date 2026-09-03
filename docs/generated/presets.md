# Hybrid Search Presets

| Preset | Text | PageRank | Status | Impact | Priority | Recency | Description |
|:---|:---:|:---:|:---:|:---:|:---:|:---:|:---|
| `default` | 0.40 | 0.20 | 0.15 | 0.10 | 0.10 | 0.05 | Balanced general-purpose search (text-led with graph context) |
| `bug-hunting` | 0.30 | 0.15 | 0.15 | 0.15 | 0.20 | 0.05 | Prioritizes open issues with high impact and recency |
| `sprint-planning` | 0.30 | 0.20 | 0.25 | 0.15 | 0.05 | 0.05 | Heavily weights PageRank and blocker impact for sprint grooming |
| `impact-first` | 0.25 | 0.30 | 0.10 | 0.20 | 0.10 | 0.05 | Centrality-first: PageRank and graph impact dominate text matches |
| `text-only` | 1.00 | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 | Pure keyword/semantic similarity with zero graph metric weighting |
