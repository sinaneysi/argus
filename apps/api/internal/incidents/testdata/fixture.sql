-- synthetic runs of an earlier installation (incidents parity, see incidents_test.go)
INSERT INTO runs (id, task, started_at, status, summary, log_path) VALUES
 (1, 'incident', 1800000000, 'FAIL', 'Checkout fails with 503', 'runs/incident-1.log'),
 (2, 'chat-triage', 1800000600, 'WARN', 'Two reports from the QA room', NULL),
 (3, 'log-rca', 1800001200, 'WARN', 'WARN `DiskSpaceLow` on db-01.example.net: /var is full. Known incident #1 covers shop.example.com.', NULL),
 (4, 'log-rca', 1800001300, 'OK', 'OK nothing found on cache.example.com', NULL),
 (5, 'incident', 1800115200, 'FAIL', 'Checkout fails again', 'runs/incident-5.log');
INSERT INTO triage_problems (run_id, idx, title, severity, environment, sources, summary) VALUES
 (1, 0, 'Checkout 503s', 'High', 'production', NULL, 'shop.example.com answers 503 to every checkout'),
 (2, 0, 'Site is slow', 'Medium', 'prod', '["QA: a reporter", "east dashboard"]', 'users say shop.example.com is slow since noon'),
 (2, 1, 'Something odd', 'Low', '', '["QA: another reporter"]', 'not clear yet'),
 (2, 2, 'Payment page timeout', 'High', 'release', '["Matrix DM: forwarded"]', 'the payment page times out for some users'),
 (5, 0, 'Checkout 503s again', 'Critical', 'production', NULL, 'shop.example.com answers 503 again');
INSERT INTO triage_actionability (run_id, idx, actionability) VALUES (2, 1, 'needs_info');
INSERT INTO alerts (run_id, active_at, severity, alertname, target) VALUES
 (3, '2027-01-15T08:00:00Z', 'warning', 'DiskSpaceLow', 'db-01.example.net:9100');
