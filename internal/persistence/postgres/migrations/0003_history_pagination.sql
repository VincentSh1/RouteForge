CREATE INDEX routeforge_requests_history_order_idx
    ON routeforge_requests (started_at DESC, request_id DESC);

DROP INDEX routeforge_requests_started_at_idx;
