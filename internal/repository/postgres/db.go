// Package postgres provides PostgreSQL+TimescaleDB implementations of all domain repository interfaces.
package postgres

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open opens a PostgreSQL+TimescaleDB connection and runs schema migrations.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Test connectivity
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if err := createHypertables(db); err != nil {
		return nil, fmt.Errorf("create hypertables: %w", err)
	}

	if err := migrateDataFromPublic(db); err != nil {
		return nil, fmt.Errorf("migrate public data: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	// ── Create new schemas ──
	_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS jagad`)
	if err != nil {
		return fmt.Errorf("create jagad schema: %w", err)
	}
	_, err = db.Exec(`CREATE SCHEMA IF NOT EXISTS metrics`)
	if err != nil {
		return fmt.Errorf("create metrics schema: %w", err)
	}
	_, err = db.Exec(`CREATE SCHEMA IF NOT EXISTS logs`)
	if err != nil {
		return fmt.Errorf("create logs schema: %w", err)
	}

	// ── Set database-level search_path ──
	// This makes unqualified table names resolve to jagad → metrics → logs → public
	// Existing installations with data in public still work; new data goes to new schemas.
	// Set for current session + persist for future connections.
	_, _ = db.Exec(`SET search_path TO "$user", jagad, metrics, logs, public`)
	_, _ = db.Exec(`ALTER DATABASE jagad SET search_path TO "$user", jagad, metrics, logs, public`)

	// ── Relational tables → jagad schema ──
	schema := `
	CREATE TABLE IF NOT EXISTS jagad.connections (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		db_type     TEXT NOT NULL CHECK(db_type IN ('postgresql', 'mysql', 'mariadb')),
		host        TEXT NOT NULL,
		port        INTEGER NOT NULL,
		username    TEXT NOT NULL,
		password    TEXT NOT NULL,
		ssl_mode    TEXT DEFAULT 'prefer',
		created_at  TIMESTAMPTZ DEFAULT NOW(),
		updated_at  TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.connection_databases (
		id              TEXT PRIMARY KEY,
		connection_id   TEXT NOT NULL REFERENCES jagad.connections(id) ON DELETE CASCADE,
		db_name         TEXT NOT NULL,
		is_selected     INTEGER DEFAULT 1,
		size_bytes      BIGINT,
		created_at      TIMESTAMPTZ DEFAULT NOW(),
		UNIQUE(connection_id, db_name)
	);

	CREATE TABLE IF NOT EXISTS jagad.storage_providers (
		id                  TEXT PRIMARY KEY,
		name                TEXT NOT NULL,
		provider_type       TEXT NOT NULL DEFAULT 's3' CHECK(provider_type IN ('s3', 'r2', 'minio', 'gcs', 'b2', 's3-compat')),
		endpoint            TEXT NOT NULL,
		region              TEXT DEFAULT 'auto',
		bucket              TEXT NOT NULL,
		access_key_encrypted BYTEA NOT NULL,
		secret_key_encrypted BYTEA NOT NULL,
		path_style          INTEGER DEFAULT 1,
		is_default          INTEGER DEFAULT 0,
		created_at          TIMESTAMPTZ DEFAULT NOW(),
		updated_at          TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.schedules (
		id              TEXT PRIMARY KEY,
		connection_id   TEXT NOT NULL REFERENCES jagad.connections(id),
		database_id     TEXT REFERENCES jagad.connection_databases(id),  -- single DB (legacy)
		database_ids    TEXT NOT NULL DEFAULT '[]',                     -- JSON array of DB IDs for multi-select
		backup_all      INTEGER NOT NULL DEFAULT 0,                    -- 1 = backup all discovered databases
		backup_type     TEXT NOT NULL CHECK(backup_type IN ('full', 'incremental')),
		cron_expr       TEXT NOT NULL,
		storage_provider_id TEXT REFERENCES jagad.storage_providers(id),
		encryption_enabled INTEGER DEFAULT 1,
		encryption_key_id TEXT,
		verify_enabled  INTEGER DEFAULT 0,
		retention_full  INTEGER DEFAULT 7,
		retention_incr  INTEGER DEFAULT 30,
		notif_target_ids TEXT DEFAULT '[]',
		notify_on_success INTEGER DEFAULT 1,
		notify_on_failure INTEGER DEFAULT 1,
		enabled         INTEGER DEFAULT 1,
		created_at      TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.backups (
		id              TEXT PRIMARY KEY,
		connection_id   TEXT NOT NULL REFERENCES jagad.connections(id),
		database_id     TEXT NOT NULL REFERENCES jagad.connection_databases(id),
		schedule_id     TEXT REFERENCES jagad.schedules(id),
		backup_type     TEXT NOT NULL CHECK(backup_type IN ('full', 'incremental')),
		status          TEXT NOT NULL CHECK(status IN ('running', 'success', 'failed', 'verifying')),
		storage_path    TEXT NOT NULL,
		storage_provider_id TEXT REFERENCES jagad.storage_providers(id),
		size_bytes      BIGINT,
		encrypted_size_bytes BIGINT,
		encryption_algo TEXT DEFAULT 'aes-256-gcm',
		encryption_key_id TEXT,
		checksum        TEXT,
		encrypted_checksum TEXT,
		verified_at     TIMESTAMPTZ,
		verify_status   TEXT CHECK(verify_status IN ('pending', 'passed', 'failed')),
		duration_ms     BIGINT,
		log_output      TEXT,
		notif_target_ids TEXT DEFAULT '[]',
		notify_on_success INTEGER DEFAULT 1,
		notify_on_failure INTEGER DEFAULT 1,
		started_at      TIMESTAMPTZ,
		completed_at    TIMESTAMPTZ,
		created_at      TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.restores (
		id              TEXT PRIMARY KEY,
		backup_id       TEXT NOT NULL REFERENCES jagad.backups(id),
		target_connection TEXT REFERENCES jagad.connections(id),
		status          TEXT NOT NULL CHECK(status IN ('running', 'success', 'failed')),
		duration_ms     BIGINT,
		log_output      TEXT,
		started_at      TIMESTAMPTZ,
		completed_at    TIMESTAMPTZ,
		created_at      TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.notifications (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		notif_type      TEXT NOT NULL CHECK(notif_type IN ('telegram', 'discord', 'slack')),
		config_json     TEXT NOT NULL,
		created_at      TIMESTAMPTZ DEFAULT NOW(),
		updated_at      TIMESTAMPTZ DEFAULT NOW()
	);

	CREATE TABLE IF NOT EXISTS jagad.encryption_keys (
		id              TEXT PRIMARY KEY,
		alias           TEXT NOT NULL UNIQUE,
		key_derivation  TEXT NOT NULL CHECK(key_derivation IN ('env', 'vault', 'manual')),
		key_salt        TEXT NOT NULL,
		key_check       TEXT NOT NULL,
		created_at      TIMESTAMPTZ DEFAULT NOW(),
		rotated_at      TIMESTAMPTZ,
		is_active       INTEGER DEFAULT 1
	);

	CREATE TABLE IF NOT EXISTS jagad.app_settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_conn_db_connection ON jagad.connection_databases(connection_id);
	CREATE INDEX IF NOT EXISTS idx_backups_connection ON jagad.backups(connection_id);
	CREATE INDEX IF NOT EXISTS idx_backups_database ON jagad.backups(database_id);
	CREATE INDEX IF NOT EXISTS idx_backups_status ON jagad.backups(status);
	CREATE INDEX IF NOT EXISTS idx_schedules_connection ON jagad.schedules(connection_id);
	`

	_, err = db.Exec(schema)
	if err != nil {
		return fmt.Errorf("create jagad tables: %w", err)
	}

	// ── Migration: add multi-DB support to schedules ──
	migrations := []string{
		`ALTER TABLE jagad.schedules ADD COLUMN IF NOT EXISTS database_ids TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE jagad.schedules ADD COLUMN IF NOT EXISTS backup_all INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE jagad.schedules ALTER COLUMN database_id DROP NOT NULL`,
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			// Log but don't fail — migration might already be partially applied
			fmt.Printf("WARN: migration '%s' failed (may already be applied): %v\n", m[:60], err)
		}
	}

	// ── Timescale tables → metrics schema ──
	// Note: The 3 main hypertables (health_checks, db_metrics, performance_metrics)
	// are created inside createHypertables(). The P2 tables are created here
	// so they can be upgraded to hypertables later.
	metricsSchema := `
	CREATE TABLE IF NOT EXISTS metrics.health_checks (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		status            TEXT,
		response_time_ms  INTEGER,
		active_connections INTEGER,
		db_size_bytes     BIGINT,
		growth_bytes      BIGINT,
		cache_hit_ratio   REAL,
		qps               INTEGER,
		connections_total INTEGER,
		query_id          TEXT,
		query_text        TEXT,
		mean_time_ms      REAL,
		total_time_ms     REAL,
		calls             INTEGER,
		rows_avg          REAL,
		db_type           TEXT,
		error_message     TEXT,
		metadata          JSONB
	);

	CREATE TABLE IF NOT EXISTS metrics.db_metrics (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		status            TEXT,
		response_time_ms  INTEGER,
		active_connections INTEGER,
		db_size_bytes     BIGINT,
		growth_bytes      BIGINT,
		cache_hit_ratio   REAL,
		qps               INTEGER,
		connections_total INTEGER,
		query_id          TEXT,
		query_text        TEXT,
		mean_time_ms      REAL,
		total_time_ms     REAL,
		calls             INTEGER,
		rows_avg          REAL,
		db_type           TEXT,
		error_message     TEXT,
		metadata          JSONB
	);

	CREATE TABLE IF NOT EXISTS metrics.performance_metrics (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		status            TEXT,
		response_time_ms  INTEGER,
		active_connections INTEGER,
		db_size_bytes     BIGINT,
		growth_bytes      BIGINT,
		cache_hit_ratio   REAL,
		qps               INTEGER,
		connections_total INTEGER,
		query_id          TEXT,
		query_text        TEXT,
		mean_time_ms      REAL,
		total_time_ms     REAL,
		calls             INTEGER,
		rows_avg          REAL,
		db_type           TEXT,
		error_message     TEXT,
		metadata          JSONB
	);

	CREATE TABLE IF NOT EXISTS metrics.autovacuum_info (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		db_type           TEXT NOT NULL DEFAULT '',
		table_name        TEXT NOT NULL DEFAULT '',
		schema_name       TEXT NOT NULL DEFAULT '',
		table_size_bytes  BIGINT DEFAULT 0,
		index_size_bytes  BIGINT DEFAULT 0,
		dead_tuples       BIGINT DEFAULT 0,
		live_tuples       BIGINT DEFAULT 0,
		dead_tuple_ratio  REAL DEFAULT 0,
		last_autovacuum   TIMESTAMPTZ,
		last_autoanalyze  TIMESTAMPTZ,
		n_auto_vacuum     BIGINT DEFAULT 0,
		n_auto_analyze    BIGINT DEFAULT 0,
		vacuum_threshold  BIGINT DEFAULT 0,
		mod_since_last_vacuum BIGINT DEFAULT 0,
		engine            TEXT NOT NULL DEFAULT '',
		table_rows        BIGINT DEFAULT 0,
		data_free_bytes   BIGINT DEFAULT 0,
		table_collation   TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS metrics.lock_info (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		db_type           TEXT NOT NULL DEFAULT '',
		database_name     TEXT NOT NULL DEFAULT '',
		relation_name     TEXT NOT NULL DEFAULT '',
		lock_type         TEXT NOT NULL DEFAULT '',
		lock_mode         TEXT NOT NULL DEFAULT '',
		granted           BOOLEAN DEFAULT TRUE,
		blocked_pid       INTEGER DEFAULT 0,
		blocked_user      TEXT NOT NULL DEFAULT '',
		blocked_query     TEXT NOT NULL DEFAULT '',
		blocked_duration_seconds REAL DEFAULT 0,
		blocking_pid      INTEGER DEFAULT 0,
		blocking_user     TEXT NOT NULL DEFAULT '',
		blocking_query    TEXT NOT NULL DEFAULT '',
		is_deadlock       BOOLEAN DEFAULT FALSE
	);

	CREATE TABLE IF NOT EXISTS metrics.replication_lag (
		time                  TIMESTAMPTZ NOT NULL,
		connection_id         TEXT NOT NULL,
		db_type               TEXT NOT NULL DEFAULT '',
		application_name      TEXT NOT NULL DEFAULT '',
		client_addr           TEXT NOT NULL DEFAULT '',
		state                 TEXT NOT NULL DEFAULT '',
		sync_state            TEXT NOT NULL DEFAULT '',
		write_lag_seconds     REAL DEFAULT 0,
		flush_lag_seconds     REAL DEFAULT 0,
		replay_lag_seconds    REAL DEFAULT 0,
		slave_io_state        TEXT NOT NULL DEFAULT '',
		slave_io_thread       TEXT NOT NULL DEFAULT '',
		slave_sql_thread      TEXT NOT NULL DEFAULT '',
		read_master_log_pos   BIGINT DEFAULT 0,
		exec_master_log_pos   BIGINT DEFAULT 0,
		relay_master_log_file TEXT NOT NULL DEFAULT '',
		seconds_behind_master INTEGER DEFAULT 0,
		last_errno            INTEGER DEFAULT 0,
		last_error            TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS metrics.table_metrics (
		time              TIMESTAMPTZ NOT NULL,
		connection_id     TEXT NOT NULL,
		db_type           TEXT NOT NULL DEFAULT '',
		database_name     TEXT NOT NULL DEFAULT '',
		schema_name       TEXT NOT NULL DEFAULT '',
		table_name        TEXT NOT NULL DEFAULT '',
		table_size_bytes  BIGINT DEFAULT 0,
		index_size_bytes  BIGINT DEFAULT 0,
		total_size_bytes  BIGINT DEFAULT 0,
		row_estimate      BIGINT DEFAULT 0,
		fill_factor       INTEGER DEFAULT 0,
		dead_tuple_ratio  REAL DEFAULT 0,
		engine            TEXT NOT NULL DEFAULT '',
	    "collation"         TEXT NOT NULL DEFAULT ''
	);

	-- Add missing columns (for existing deployments that had partial migration)
	ALTER TABLE IF EXISTS metrics.db_metrics ADD COLUMN IF NOT EXISTS max_connections INTEGER DEFAULT 0;
	ALTER TABLE IF EXISTS metrics.db_metrics ADD COLUMN IF NOT EXISTS conn_usage_percent REAL DEFAULT 0;

	ALTER TABLE IF EXISTS jagad.connections ADD COLUMN IF NOT EXISTS db_version TEXT DEFAULT '';
	`

	_, err = db.Exec(metricsSchema)
	if err != nil {
		return fmt.Errorf("create metrics tables: %w", err)
	}

	// ── Audit log table → logs schema ──
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS logs.audit_logs (
			id              BIGSERIAL PRIMARY KEY,
			actor_id        TEXT NOT NULL DEFAULT 'system',
			action          TEXT NOT NULL,
			target_type     TEXT NOT NULL DEFAULT '',
			target_id       TEXT NOT NULL DEFAULT '',
			metadata        JSONB DEFAULT '{}',
			ip_address      INET,
			created_at      TIMESTAMPTZ DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON logs.audit_logs(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON logs.audit_logs(action);
		CREATE INDEX IF NOT EXISTS idx_audit_logs_actor ON logs.audit_logs(actor_id);
	`)
	if err != nil {
		return fmt.Errorf("create logs.audit_logs: %w", err)
	}

	// ── Seed default settings ──
	defaults := map[string]string{
		"retention_full_default": "7",
		"retention_incr_default": "30",
		"concurrent_backups":     "2",
		"compression":            "gzip",
		"timezone":               "UTC",
		"notify_on_success":      "true",
		"notify_on_failure":      "true",
	}
	for k, v := range defaults {
		_, _ = db.Exec(`INSERT INTO jagad.app_settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, k, v)
	}

	return nil
}

func createHypertables(db *sql.DB) error {
	// Check if TimescaleDB extension is available
	var extExists bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'timescaledb')`).Scan(&extExists)
	if err != nil {
		return nil
	}
	if !extExists {
		return nil
	}

	hypertables := []struct {
		table string
		col   string
	}{
		{"metrics.health_checks", "time"},
		{"metrics.db_metrics", "time"},
		{"metrics.performance_metrics", "time"},
		{"metrics.autovacuum_info", "time"},
		{"metrics.lock_info", "time"},
		{"metrics.replication_lag", "time"},
		{"metrics.table_metrics", "time"},
	}

	for _, ht := range hypertables {
		// Convert to hypertable — safe to call even if already hypertable
		_, err = db.Exec(fmt.Sprintf(`SELECT create_hypertable('%s', '%s', if_not_exists => TRUE)`, ht.table, ht.col))
		if err != nil {
			continue
		}

		// Add compression policy (compress data older than 7 days)
		_, _ = db.Exec(fmt.Sprintf(`SELECT add_compression_policy('%s', INTERVAL '7 days', if_not_exists => TRUE)`, ht.table))

		// Add retention policy (delete data older than 90 days)
		_, _ = db.Exec(fmt.Sprintf(`SELECT add_retention_policy('%s', INTERVAL '90 days', if_not_exists => TRUE)`, ht.table))
	}

	return nil
}

// migrateDataFromPublic migrates data from old public schema tables to new schema
// if the old public tables exist and the new tables are empty (fresh migration).
// This ensures backward compatibility for existing deployments.
func migrateDataFromPublic(db *sql.DB) error {
	tables := []struct {
		src string // old public.table
		dst string // new schema.table
		cols string
	}{
		{"public.connections", "jagad.connections", "id, name, db_type, host, port, username, password, ssl_mode, created_at, updated_at"},
		{"public.connection_databases", "jagad.connection_databases", "id, connection_id, db_name, is_selected, size_bytes, created_at"},
		{"public.storage_providers", "jagad.storage_providers", "id, name, provider_type, endpoint, region, bucket, access_key_encrypted, secret_key_encrypted, path_style, is_default, created_at, updated_at"},
		{"public.schedules", "jagad.schedules", "id, connection_id, database_id, backup_type, cron_expr, storage_provider_id, encryption_enabled, encryption_key_id, verify_enabled, retention_full, retention_incr, notif_target_ids, notify_on_success, notify_on_failure, enabled, created_at"},
		{"public.backups", "jagad.backups", "id, connection_id, database_id, schedule_id, backup_type, status, storage_path, storage_provider_id, size_bytes, encrypted_size_bytes, encryption_algo, encryption_key_id, checksum, encrypted_checksum, verified_at, verify_status, duration_ms, log_output, notif_target_ids, notify_on_success, notify_on_failure, started_at, completed_at, created_at"},
		{"public.restores", "jagad.restores", "id, backup_id, target_connection, status, duration_ms, log_output, started_at, completed_at, created_at"},
		{"public.notifications", "jagad.notifications", "id, name, notif_type, config_json, created_at, updated_at"},
		{"public.encryption_keys", "jagad.encryption_keys", "id, alias, key_derivation, key_salt, key_check, created_at, rotated_at, is_active"},
		{"public.app_settings", "jagad.app_settings", "key, value"},
	}

	for _, t := range tables {
		err := migrateTableIfNeeded(db, t.src, t.dst, t.cols)
		if err != nil {
			return err
		}
	}

	// Also migrate hypertables from public → metrics
	metricsTables := []struct {
		src  string
		dst  string
		cols string
	}{
		{"public.health_checks", "metrics.health_checks", "time, connection_id, status, response_time_ms, active_connections, error_message"},
		{"public.db_metrics", "metrics.db_metrics", "time, connection_id, db_type, db_size_bytes, growth_bytes, cache_hit_ratio, qps, connections_total, max_connections, conn_usage_percent"},
		{"public.performance_metrics", "metrics.performance_metrics", "time, connection_id, db_type, query_id, query_text, mean_time_ms, total_time_ms, calls, rows_avg"},
	}

	for _, t := range metricsTables {
		_ = migrateTableIfNeeded(db, t.src, t.dst, t.cols)
	}

	return nil
}

func migrateTableIfNeeded(db *sql.DB, src, dst, cols string) error {
	// Check if source table exists in public schema
	var srcExists bool
	err := db.QueryRow(fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = '%s')`, extractTableName(src))).Scan(&srcExists)
	if err != nil || !srcExists {
		return nil // source doesn't exist, nothing to migrate
	}

	// Check if destination already has data
	var dstCount int
	err = db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, dst)).Scan(&dstCount)
	if err != nil {
		return nil // destination doesn't exist or error
	}
	if dstCount > 0 {
		return nil // already migrated
	}

	// Check if source has data
	var srcCount int
	err = db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, extractTableName(src))).Scan(&srcCount)
	if err != nil || srcCount == 0 {
		return nil // nothing to migrate
	}

	// Migrate: INSERT into new schema from old
	_, err = db.Exec(fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM %s`, dst, cols, cols, src))
	if err != nil {
		return fmt.Errorf("migrate %s → %s: %w", src, dst, err)
	}

	return nil
}

// extractTableName strips schema prefix for information_schema lookups.
func extractTableName(table string) string {
	for i := len(table) - 1; i >= 0; i-- {
		if table[i] == '.' {
			return table[i+1:]
		}
	}
	return table
}
