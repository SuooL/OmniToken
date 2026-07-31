use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct ModelUsage {
    pub model: String,
    pub tokens: u64,
    pub share: f64,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct TodayUsage {
    pub start_ms: i64,
    pub end_ms: i64,
    pub total_tokens: u64,
    pub models: Vec<ModelUsage>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct SourceUsage {
    pub source: String,
    pub tokens: u64,
    pub previous_tokens: u64,
    pub change_percent: Option<f64>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct RollingUsage {
    pub start_ms: i64,
    pub end_ms: i64,
    pub total_tokens: u64,
    pub sources: Vec<SourceUsage>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct SourceContribution {
    #[serde(rename(deserialize = "key", serialize = "source"))]
    pub source: String,
    pub contribution_tps: f64,
    #[serde(default)]
    pub native_tps: Option<f64>,
    #[serde(default)]
    pub output_tokens: u64,
    #[serde(default)]
    pub active_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct SpeedBucket {
    pub start_ms: i64,
    pub aggregate_tps: f64,
    pub active_ms: i64,
    pub sources: Vec<SourceContribution>,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub struct SpeedTelemetry {
    pub bucket_ms: i64,
    pub measured_sources: Vec<String>,
    pub unmeasured_sources: Vec<String>,
    pub series: Vec<SpeedBucket>,
    pub aggregate_10m_tps: Option<f64>,
    pub peak_tps: Option<f64>,
    pub peak_at: Option<i64>,
    pub active_ratio: Option<f64>,
}

#[derive(Clone, Debug, Deserialize)]
struct WireTelemetry {
    generated_at: i64,
    timezone: String,
    today: TodayUsage,
    rolling_5h: RollingUsage,
    speed: SpeedTelemetry,
}

/// Presentation-neutral values exposed to the webview.
///
/// The four trailing fields describe the Rust fetch, not the metric itself.
/// They let the popover retain same-endpoint last-good values without making a
/// failed refresh look current.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct TelemetryView {
    pub generated_at_ms: i64,
    pub timezone: String,
    pub today: TodayUsage,
    pub rolling_5h: RollingUsage,
    pub speed: SpeedTelemetry,
    pub fetched_at_ms: i64,
    pub age_ms: i64,
    pub is_stale: bool,
    pub error: Option<String>,
}

fn finite_non_negative(value: f64) -> bool {
    value.is_finite() && value >= 0.0
}

fn valid_time(value: i64) -> bool {
    value >= 0
}

fn validate(raw: &WireTelemetry) -> Result<(), String> {
    if !valid_time(raw.generated_at)
        || !valid_time(raw.today.start_ms)
        || !valid_time(raw.today.end_ms)
        || !valid_time(raw.rolling_5h.start_ms)
        || !valid_time(raw.rolling_5h.end_ms)
        || raw.speed.bucket_ms <= 0
    {
        return Err("telemetry contains an invalid timestamp or bucket".into());
    }
    if raw.today.end_ms < raw.today.start_ms || raw.rolling_5h.end_ms < raw.rolling_5h.start_ms {
        return Err("telemetry window ends before it starts".into());
    }
    if raw.timezone.trim().is_empty() {
        return Err("telemetry timezone is empty".into());
    }
    if raw.today.models.iter().any(|row| {
        row.model.trim().is_empty() || !finite_non_negative(row.share) || row.share > 1.0
    }) {
        return Err("telemetry today model is invalid".into());
    }
    let model_tokens = raw
        .today
        .models
        .iter()
        .try_fold(0_u64, |sum, row| sum.checked_add(row.tokens))
        .ok_or_else(|| "telemetry today models overflow".to_string())?;
    if model_tokens != raw.today.total_tokens {
        return Err("telemetry today models do not sum to total".into());
    }
    if raw.rolling_5h.sources.iter().any(|row| {
        row.source.trim().is_empty() || row.change_percent.is_some_and(|value| !value.is_finite())
    }) {
        return Err("telemetry rolling source is invalid".into());
    }
    if raw
        .speed
        .aggregate_10m_tps
        .is_some_and(|value| !finite_non_negative(value))
        || raw
            .speed
            .peak_tps
            .is_some_and(|value| !finite_non_negative(value))
        || raw
            .speed
            .active_ratio
            .is_some_and(|value| !value.is_finite() || !(0.0..=1.0).contains(&value))
        || raw.speed.peak_at.is_some_and(|value| !valid_time(value))
    {
        return Err("telemetry speed summary is invalid".into());
    }
    for bucket in &raw.speed.series {
        if !valid_time(bucket.start_ms)
            || bucket.active_ms < 0
            || !finite_non_negative(bucket.aggregate_tps)
        {
            return Err("telemetry speed bucket is invalid".into());
        }
        if bucket.sources.iter().any(|source| {
            source.source.trim().is_empty()
                || source.active_ms < 0
                || !finite_non_negative(source.contribution_tps)
                || source
                    .native_tps
                    .is_some_and(|value| !finite_non_negative(value))
        }) {
            return Err("telemetry source contribution is invalid".into());
        }
        let contributions = bucket
            .sources
            .iter()
            .map(|source| source.contribution_tps)
            .sum::<f64>();
        if (bucket.aggregate_tps - contributions).abs() > 1e-6 {
            return Err(format!(
                "telemetry source contributions do not reconcile: aggregate={} sum={contributions}",
                bucket.aggregate_tps
            ));
        }
    }
    Ok(())
}

fn parse_payload(payload: Value) -> Result<TelemetryView, String> {
    let raw: WireTelemetry =
        serde_json::from_value(payload).map_err(|error| format!("invalid telemetry: {error}"))?;
    validate(&raw)?;
    Ok(TelemetryView {
        generated_at_ms: raw.generated_at,
        timezone: raw.timezone,
        today: raw.today,
        rolling_5h: raw.rolling_5h,
        speed: raw.speed,
        fetched_at_ms: 0,
        age_ms: 0,
        is_stale: false,
        error: None,
    })
}

fn endpoint_identity(endpoint: &str) -> String {
    endpoint.trim_end_matches('/').to_string()
}

#[derive(Default)]
struct Cache {
    endpoint: Option<String>,
    view: Option<TelemetryView>,
}

impl Cache {
    fn bind_endpoint(&mut self, endpoint: &str) {
        let endpoint = endpoint_identity(endpoint);
        if self.endpoint.as_deref() != Some(endpoint.as_str()) {
            self.endpoint = Some(endpoint);
            self.view = None;
        }
    }

    fn record_success(
        &mut self,
        endpoint: &str,
        mut view: TelemetryView,
        fetched_at_ms: i64,
    ) -> bool {
        let endpoint = endpoint_identity(endpoint);
        if self.endpoint.is_none() {
            self.endpoint = Some(endpoint.clone());
        }
        if self.endpoint.as_deref() != Some(endpoint.as_str()) {
            return false;
        }
        view.fetched_at_ms = fetched_at_ms;
        view.age_ms = fetched_at_ms.saturating_sub(view.generated_at_ms);
        view.is_stale = false;
        view.error = None;
        self.view = Some(view);
        true
    }

    fn current(&self) -> Option<TelemetryView> {
        self.view.clone()
    }

    fn last_good(&self, now_ms: i64, error: &str) -> Option<TelemetryView> {
        self.view.clone().map(|mut view| {
            view.age_ms = now_ms.saturating_sub(view.generated_at_ms);
            view.is_stale = true;
            view.error = Some(error.to_string());
            view
        })
    }

    fn last_good_for(&self, endpoint: &str, now_ms: i64, error: &str) -> Option<TelemetryView> {
        (self.endpoint.as_deref() == Some(endpoint_identity(endpoint).as_str()))
            .then(|| self.last_good(now_ms, error))
            .flatten()
    }

    fn clear_for_unauthorized(&mut self, endpoint: &str) {
        if self.endpoint.as_deref() == Some(endpoint_identity(endpoint).as_str()) {
            self.view = None;
        }
    }
}

#[derive(Default)]
pub struct State(Mutex<Cache>);

fn wall_now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

pub async fn get(
    app: tauri::AppHandle,
    state: tauri::State<'_, State>,
    range: String,
) -> Result<TelemetryView, String> {
    if !matches!(range.as_str(), "1h" | "5h" | "24h") {
        return Err("range must be one of 1h, 5h, or 24h".into());
    }
    let settings = crate::settings::load(&app);
    let endpoint = endpoint_identity(&settings.server);
    state.0.lock().unwrap().bind_endpoint(&endpoint);

    let path = format!("/api/v1/telemetry?range={range}");
    let fetched = crate::get_json(&endpoint, &path, &settings.token).await;
    let now = wall_now_ms();
    match fetched.and_then(|payload| parse_payload(payload).map_err(crate::FetchError::Other)) {
        Ok(view) => {
            let mut cache = state.0.lock().unwrap();
            if cache.record_success(&endpoint, view, now) {
                cache
                    .current()
                    .ok_or_else(|| "telemetry cache publication failed".to_string())
            } else {
                Err("telemetry endpoint changed while the request was in flight".into())
            }
        }
        Err(crate::FetchError::Unauthorized(message)) => {
            state.0.lock().unwrap().clear_for_unauthorized(&endpoint);
            Err(message)
        }
        Err(error) => state
            .0
            .lock()
            .unwrap()
            .last_good_for(&endpoint, now, &error.to_string())
            .ok_or_else(|| error.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn complete_payload() -> serde_json::Value {
        json!({
            "generated_at": 1_785_460_000_000_i64,
            "timezone": "America/New_York",
            "today": {
                "start_ms": 1_785_427_200_000_i64,
                "end_ms": 1_785_460_000_000_i64,
                "total_tokens": 6_920_000,
                "models": [
                    {"model":"claude-sonnet","tokens":3_040_000,"share":0.4393},
                    {"model":"gpt-5","tokens":2_010_000,"share":0.2905},
                    {"model":"claude-opus","tokens":1_870_000,"share":0.2702}
                ]
            },
            "rolling_5h": {
                "start_ms": 1_785_442_000_000_i64,
                "end_ms": 1_785_460_000_000_i64,
                "total_tokens": 4_510_000,
                "sources": [
                    {"source":"claude-code","tokens":2_840_000,"previous_tokens":2_410_000,"change_percent":17.84},
                    {"source":"codex","tokens":1_370_000,"previous_tokens":0,"change_percent":null},
                    {"source":"api","tokens":300_000,"previous_tokens":250_000,"change_percent":20.0}
                ]
            },
            "speed": {
                "bucket_ms": 60_000,
                "measured_sources": ["claude-code", "api"],
                "unmeasured_sources": ["codex"],
                "series": [{
                    "start_ms": 1_785_456_400_000_i64,
                    "aggregate_tps": 60.2,
                    "active_ms": 42_000,
                    "sources": [
                        {"key":"claude-code","contribution_tps":48.2,"native_tps":54.6,"output_tokens":2024},
                        {"key":"api","contribution_tps":12.0,"native_tps":18.5,"output_tokens":504}
                    ]
                }],
                "aggregate_10m_tps": 92.1,
                "peak_tps": 124.0,
                "peak_at": 1_785_459_720_000_i64,
                "active_ratio": 0.71
            }
        })
    }

    #[test]
    fn telemetry_keeps_every_today_model_and_unknown_codex_speed() {
        let view = parse_payload(complete_payload()).expect("valid telemetry");

        assert_eq!(view.today.models.len(), 3);
        assert_eq!(view.today.models[2].model, "claude-opus");
        assert_eq!(view.speed.unmeasured_sources, ["codex"]);
        assert!(view
            .speed
            .series
            .iter()
            .flat_map(|bucket| &bucket.sources)
            .all(|source| source.source != "codex"));
    }

    #[test]
    fn telemetry_requires_source_contributions_to_reconcile() {
        let mut payload = complete_payload();
        payload["speed"]["series"][0]["aggregate_tps"] = json!(99.0);

        let error = parse_payload(payload).unwrap_err();
        assert!(error.contains("reconcile"), "{error}");
    }

    #[test]
    fn telemetry_rejects_malformed_or_invalid_numeric_payloads() {
        let mut negative = complete_payload();
        negative["today"]["total_tokens"] = json!(-1);
        assert!(parse_payload(negative).is_err());

        let mut missing = complete_payload();
        missing["speed"].as_object_mut().unwrap().remove("series");
        assert!(parse_payload(missing).is_err());
    }

    #[test]
    fn telemetry_rejects_an_incomplete_today_model_list() {
        let mut incomplete = complete_payload();
        incomplete["today"]["models"].as_array_mut().unwrap().pop();

        let error = parse_payload(incomplete).unwrap_err();
        assert!(error.contains("today models"), "{error}");
    }

    #[test]
    fn endpoint_change_discards_last_good_telemetry() {
        let mut cache = Cache::default();
        let accepted = parse_payload(complete_payload()).unwrap();
        cache.record_success("http://server-a/", accepted, 2_000);

        cache.bind_endpoint("http://server-b");

        assert!(cache.last_good(2_500, "offline").is_none());
    }

    #[test]
    fn late_failure_from_previous_endpoint_cannot_stale_new_telemetry() {
        let mut cache = Cache::default();
        cache.bind_endpoint("http://server-a");
        cache.bind_endpoint("http://server-b");
        let accepted = parse_payload(complete_payload()).unwrap();
        cache.record_success("http://server-b", accepted, 1_785_460_000_100);

        assert!(cache
            .last_good_for("http://server-a", 1_785_460_000_850, "old endpoint failed")
            .is_none());
        assert!(!cache.current().unwrap().is_stale);
    }

    #[test]
    fn failed_refresh_returns_same_endpoint_last_good_with_freshness() {
        let mut cache = Cache::default();
        let accepted = parse_payload(complete_payload()).unwrap();
        cache.record_success("http://server-a", accepted, 1_785_460_000_100);

        let stale = cache
            .last_good(1_785_460_000_850, "connection refused")
            .expect("same endpoint keeps last-good telemetry");

        assert_eq!(stale.fetched_at_ms, 1_785_460_000_100);
        assert_eq!(stale.age_ms, 850);
        assert!(stale.is_stale);
        assert_eq!(stale.error.as_deref(), Some("connection refused"));
    }

    #[test]
    fn unauthorized_refresh_discards_sensitive_last_good_values() {
        let mut cache = Cache::default();
        let accepted = parse_payload(complete_payload()).unwrap();
        cache.record_success("http://server-a", accepted, 1_785_460_000_100);

        cache.clear_for_unauthorized("http://server-a");

        assert!(cache.current().is_none());
        assert!(cache
            .last_good_for("http://server-a", 1_785_460_000_850, "401")
            .is_none());
    }
}
