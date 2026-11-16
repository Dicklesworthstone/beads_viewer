/*
 * HybridScorer - Client-side graph-aware search ranking
 * Mirrors pkg/search/hybrid_scorer_impl.go normalization logic.
 */

const HYBRID_PRESETS = {
  default: { text: 0.40, pagerank: 0.20, status: 0.15, impact: 0.10, priority: 0.10, recency: 0.05 },
  'bug-hunting': { text: 0.30, pagerank: 0.15, status: 0.15, impact: 0.15, priority: 0.20, recency: 0.05 },
  'sprint-planning': { text: 0.30, pagerank: 0.20, status: 0.25, impact: 0.15, priority: 0.05, recency: 0.05 },
  'impact-first': { text: 0.25, pagerank: 0.30, status: 0.10, impact: 0.20, priority: 0.10, recency: 0.05 },
  'text-only': { text: 1.00, pagerank: 0.00, status: 0.00, impact: 0.00, priority: 0.00, recency: 0.00 }
};

class HybridScorer {
  constructor(weights = HYBRID_PRESETS.default, options = {}) {
    this.weights = { ...weights };
    this.maxBlockerCount = 0;
    const configuredMax = options?.maxBlockerCount;
    this.configuredMaxBlockerCount = (
      typeof configuredMax === 'number' &&
      Number.isFinite(configuredMax) &&
      configuredMax >= 0
    ) ? configuredMax : null;
  }

  resolveReferenceNowMs() {
    const viewerNow = typeof window !== 'undefined' ? window.beadsViewer?.getReferenceNowMs?.() : undefined;
    if (typeof viewerNow === 'number' && Number.isFinite(viewerNow) && viewerNow > 0) {
      return viewerNow;
    }
    const winNow = typeof window !== 'undefined' ? window.BV_REFERENCE_NOW_MS : undefined;
    if (typeof winNow === 'number' && Number.isFinite(winNow) && winNow > 0) {
      return winNow;
    }
    return Date.now();
  }

  resolveEffectiveStatus(result, refNowMs = this.resolveReferenceNowMs()) {
    const normStatus = String(result?.status || '').trim().toLowerCase();
    if (normStatus === 'closed' || normStatus === 'tombstone') {
      return normStatus;
    }
    const isTruthy = (v) => v === true || v === 'true' || Number(v) > 0;
    if (
      isTruthy(result?.is_template ?? result?.isTemplate) ||
      isTruthy(result?.pinned ?? result?.isPinned) ||
      normStatus === 'pinned'
    ) {
      return 'deferred';
    }
    const deferRaw = result?.defer_until ?? result?.deferUntil;
    if ((normStatus === 'open' || normStatus === 'in_progress') && deferRaw) {
      const deferMs = new Date(deferRaw).getTime();
      if (Number.isFinite(deferMs) && deferMs > refNowMs) {
        return 'deferred';
      }
    }
    const depState = String(result?.dependency_state ?? result?.dependencyState ?? '').trim().toLowerCase();
    const isDepBlocked = (depState !== '' && depState !== 'satisfied') || Number(result?.open_blocker_count ?? 0) > 0;
    if ((normStatus === 'open' || normStatus === 'in_progress') && isDepBlocked) {
      return 'blocked';
    }
    return normStatus;
  }

  score(result, refNowMs = this.resolveReferenceNowMs()) {
    const statusScore = this.normalizeStatus(this.resolveEffectiveStatus(result, refNowMs));
    const priorityScore = this.normalizePriority(result.priority);
    const rawBlocker = Number(result.blockerCount ?? result.impact ?? result.blocks_count ?? result.dependent_count ?? 0);
    const downstreamImpact = Number.isFinite(rawBlocker) ? Math.max(0, Math.trunc(rawBlocker)) : 0;
    const impactScore = this.normalizeImpact(downstreamImpact);
    const recencyScore = this.normalizeRecency(result.updatedAt ?? result.updated_at, refNowMs);
    const pagerank = (typeof result.pagerank === 'number' && Number.isFinite(result.pagerank)) ? result.pagerank : 0.5;
    const rawText = Number(result.textScore ?? result.text_score ?? 0);
    const textScore = Number.isFinite(rawText) ? rawText : 0;

    const finalScore =
      this.weights.text * textScore +
      this.weights.pagerank * pagerank +
      this.weights.status * statusScore +
      this.weights.impact * impactScore +
      this.weights.priority * priorityScore +
      this.weights.recency * recencyScore;

    return {
      ...result,
      hybrid_score: finalScore,
      component_scores: {
        pagerank,
        status: statusScore,
        impact: impactScore,
        priority: priorityScore,
        recency: recencyScore,
      },
    };
  }

  scoreAndRank(results) {
    if (!Array.isArray(results) || results.length === 0) {
      return [];
    }
    const refNowMs = this.resolveReferenceNowMs();
    const maxBlocker = results.reduce(
      (max, r) => {
        const val = Number(r.blockerCount ?? r.impact ?? r.blocks_count ?? r.dependent_count ?? 0);
        return Math.max(max, Number.isFinite(val) ? Math.max(0, Math.trunc(val)) : 0);
      },
      0
    );
    this.maxBlockerCount = this.configuredMaxBlockerCount !== null
      ? Math.max(maxBlocker, Math.max(0, Math.trunc(this.configuredMaxBlockerCount)))
      : maxBlocker;

    return results
      .map(r => this.score(r, refNowMs))
      .sort((a, b) => {
        const diff = b.hybrid_score - a.hybrid_score;
        if (diff !== 0) return diff;
        const aid = String(a.id ?? a.issue_id ?? '');
        const bid = String(b.id ?? b.issue_id ?? '');
        return aid < bid ? -1 : (aid > bid ? 1 : 0);
      });
  }

  normalizeStatus(status) {
    const normalized = String(status || '').toLowerCase().trim();
    const STATUS_WEIGHTS = {
      open: 1.0,
      in_progress: 0.8,
      review: 0.8,
      hooked: 0.8,
      pinned: 0.2,
      blocked: 0.5,
      deferred: 0.2,
      draft: 0.2,
      closed: 0.1,
      tombstone: 0.0,
    };
    return STATUS_WEIGHTS[normalized] ?? 0.5;
  }

  normalizePriority(priority) {
    const PRIORITY_WEIGHTS = [1.0, 0.8, 0.6, 0.4, 0.2];
    if (typeof priority === 'number' && Number.isInteger(priority) && priority >= 0 && priority <= 4) {
      return PRIORITY_WEIGHTS[priority];
    }
    if (typeof priority === 'string' && /^[0-4]$/.test(priority.trim())) {
      return PRIORITY_WEIGHTS[Number(priority.trim())];
    }
    return 0.5;
  }

  normalizeImpact(blockerCount) {
    if (this.maxBlockerCount === 0) return 0.5;
    if (blockerCount <= 0) return 0;
    if (blockerCount >= this.maxBlockerCount) return 1.0;
    return blockerCount / this.maxBlockerCount;
  }

  normalizeRecency(updatedAt, refNow = this.resolveReferenceNowMs()) {
    if (!updatedAt) return 0.5;
    const ts = new Date(updatedAt).getTime();
    if (!Number.isFinite(ts)) return 0.5;
    const daysSince = Math.max(0, (refNow - ts) / (1000 * 60 * 60 * 24));
    return Math.exp(-daysSince / 30);
  }
}

window.HybridScorer = HybridScorer;
window.HYBRID_PRESETS = HYBRID_PRESETS;
