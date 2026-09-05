CREATE TABLE routeforge_requests (
    request_id VARCHAR(64) PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    routing_policy VARCHAR(32) NOT NULL,
    streaming BOOLEAN NOT NULL,
    logical_model VARCHAR(256) NOT NULL,
    initial_provider VARCHAR(64),
    final_provider VARCHAR(64),
    outcome VARCHAR(32) NOT NULL CHECK (outcome IN (
        'success', 'cancellation', 'timeout', 'unavailable', 'rate_limited',
        'invalid_request', 'internal', 'other_failure'
    )),
    attempt_count INTEGER NOT NULL CHECK (attempt_count >= 0),
    fallback_count INTEGER NOT NULL CHECK (fallback_count >= 0 AND fallback_count <= attempt_count),
    request_duration_us BIGINT NOT NULL CHECK (request_duration_us >= 0),
    CHECK (completed_at >= started_at)
);

CREATE INDEX routeforge_requests_started_at_idx
    ON routeforge_requests (started_at DESC);

CREATE TABLE routeforge_provider_attempts (
    request_id VARCHAR(64) NOT NULL REFERENCES routeforge_requests(request_id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    provider VARCHAR(64) NOT NULL,
    resolved_provider_model VARCHAR(256) NOT NULL,
    fallback BOOLEAN NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    duration_us BIGINT NOT NULL CHECK (duration_us >= 0),
    ttfc_us BIGINT CHECK (ttfc_us >= 0),
    outcome VARCHAR(32) NOT NULL CHECK (outcome IN (
        'success', 'cancellation', 'timeout', 'unavailable', 'rate_limited',
        'invalid_request', 'internal', 'other_failure'
    )),
    input_tokens BIGINT CHECK (input_tokens >= 0),
    output_tokens BIGINT CHECK (output_tokens >= 0),
    total_tokens BIGINT CHECK (total_tokens >= 0),
    estimated_cost_micro_usd BIGINT CHECK (estimated_cost_micro_usd >= 0),
    PRIMARY KEY (request_id, attempt_number),
    CHECK (completed_at >= started_at),
    CHECK (ttfc_us IS NULL OR ttfc_us <= duration_us)
);
