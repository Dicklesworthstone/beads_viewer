# Sort Modes

| Mode | Key Display | Ordering Logic | Use Case |
|:---|:---:|:---|:---|
| **Default** | `Default` | Active before closed → Priority (asc) → Created (desc) | Standard priority-driven workflow |
| **Created ↑** | `Created ↑` | Creation date ascending (oldest first) | Audit: find long-standing issues |
| **Created ↓** | `Created ↓` | Creation date descending (newest first) | Review: see recently created work |
| **Priority** | `Priority` | Priority only (P0 → P4) | Pure priority triage |
| **Updated** | `Updated` | Last update descending (newest first) | Activity tracking: see active issues |
| **Triage ↓** | `Triage ↓` | Active before closed/pinned/template → Triage impact score (desc) → Priority (asc) | Graph-weighted impact prioritization |
| **Unblocks ↓** | `Unblocks ↓` | Active before closed → Direct unblock count (desc) → Priority (asc) | Bottleneck clearing: maximize unblocked work |
| **Risk ↓** | `Risk ↓` | Operational risk score (desc) → Active before closed → Priority (asc) | Risk management: surface highest-risk items |
| **ETA ↑** | `ETA ↑` | Estimated days to completion (asc) → Active before closed → Priority (asc) | Quick-win focus: soonest estimated completion |
