"use strict";

// Last-good, generation-safe cache for the bounded telemetry endpoint.
const TelemetryCache = {
  _entries: new Map(),

  entry(range = "5h") {
    if (!this._entries.has(range)) {
      this._entries.set(range, {
        generation: 0, data: null, receivedAt: 0, error: null, promise: null,
      });
    }
    return this._entries.get(range);
  },

  peek(range = "5h") {
    const entry = this.entry(range);
    return {
      data: entry.data,
      stale: Boolean(entry.error && entry.data),
      error: entry.error,
      receivedAt: entry.receivedAt,
      ageMS: entry.receivedAt ? Math.max(0, Date.now() - entry.receivedAt) : null,
    };
  },

  async load(range = "5h", { force = false } = {}) {
    const entry = this.entry(range);
    if (entry.promise && !force) return entry.promise;
    const generation = ++entry.generation;
    const request = Api.get(`/api/v1/telemetry?range=${encodeURIComponent(range)}`)
      .then((data) => {
        if (entry.generation !== generation) return this.peek(range);
        entry.data = data;
        entry.receivedAt = Date.now();
        entry.error = null;
        return this.peek(range);
      })
      .catch((error) => {
        if (entry.generation === generation) {
          entry.error = error;
          if (error && error.status === 401) {
            entry.data = null;
            entry.receivedAt = 0;
            throw error;
          }
        }
        if (entry.data) return this.peek(range);
        throw error;
      })
      .finally(() => {
        if (entry.generation === generation) entry.promise = null;
      });
    entry.promise = request;
    return request;
  },

  invalidate(range) {
    const entries = range ? [this.entry(range)] : [...this._entries.values()];
    entries.forEach((entry) => {
      entry.generation += 1;
      entry.promise = null;
    });
  },
};

function telemetrySpeed(snapshot) {
  return snapshot && snapshot.speed || {
    series: [], measured_sources: [], unmeasured_sources: [],
  };
}

function telemetrySourceRows(snapshot) {
  return snapshot && snapshot.rolling_5h && snapshot.rolling_5h.sources || [];
}

function speedSourceKey(row) {
  return row && (row.source || row.key) || "other";
}

function currentAggregateTPS(liveSpeed) {
  if (Number.isFinite(liveSpeed && liveSpeed.tps)) return liveSpeed.tps;
  return null;
}

function sourceLabelA2(source) {
  return {
    "claude-code": "Claude",
    codex: "Codex",
    api: "Other/API",
  }[source] || sourceLabel(source);
}
