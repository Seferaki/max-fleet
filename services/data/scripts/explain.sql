-- EXPLAIN основных выборок DE-08 (запускать после нагрузки/seed; только чтение).
\echo '== active trip for employee'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT id FROM trips
 WHERE employee_id = (SELECT id FROM employees ORDER BY id LIMIT 1) AND status IN ('active','returning');
\echo '== my trips page'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM trips
 WHERE employee_id = (SELECT id FROM employees ORDER BY id LIMIT 1) ORDER BY started_at DESC, id DESC LIMIT 5;
\echo '== vehicle history page'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM trips
 WHERE vehicle_id = '10000000-0000-4000-8000-000000000001' ORDER BY started_at DESC, id DESC LIMIT 5;
\echo '== open issues'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM issues WHERE status = 'open' ORDER BY created_at LIMIT 50;
\echo '== due notifications'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM notification_deliveries
 WHERE status IN ('pending','sending','retry') ORDER BY next_attempt_at LIMIT 50;
\echo '== inbox claim candidates'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM inbound_events
 WHERE status IN ('pending','processing','retry') ORDER BY sequence LIMIT 500;
\echo '== due holds'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT id FROM checkout_attempts
 WHERE status = 'holding' AND expires_at <= now() ORDER BY expires_at LIMIT 20;
\echo '== idempotency lookup'
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT * FROM idempotency_records WHERE scope = 'command:1' AND key = 'x';
SELECT relname, n_live_tup FROM pg_stat_user_tables ORDER BY n_live_tup DESC LIMIT 8;
