// Package monitoring provides database health/performance monitoring services.
// It queries the managed source databases (not the local config DB) for metrics.
package monitoring

import (
	"database/sql"
	"time"
)

// HealthCheck represents a health check result for a database connection.
type HealthCheck struct {
	Time              time.Time `json:"time"`
	ConnectionID      string    `json:"connection_id"`
	Status            string    `json:"status"` // healthy, degraded, down
	ResponseTimeMs    int       `json:"response_time_ms"`
	ActiveConnections int       `json:"active_connections"`
	ErrorMessage      string    `json:"error_message,omitempty"`
}

// DBMetric represents database-level metrics (size, growth, etc.).
type DBMetric struct {
	Time              time.Time `json:"time"`
	ConnectionID      string    `json:"connection_id"`
	DBName            string    `json:"db_name"`
	DBType            string    `json:"db_type"`
	DBSizeBytes       int64     `json:"db_size_bytes"`
	GrowthBytes       int64     `json:"growth_bytes"`
	CacheHitRatio     float64   `json:"cache_hit_ratio"`
	QPS               int       `json:"qps"`
	ConnectionsTotal  int       `json:"connections_total"`
	MaxConnections    int       `json:"max_connections"`
	ConnUsagePercent  float64   `json:"conn_usage_percent"`
}

// PerformanceMetric represents a slow query or query performance snapshot.
type PerformanceMetric struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connection_id"`
	DBType       string    `json:"db_type"`
	QueryID      string    `json:"query_id"`
	QueryText    string    `json:"query_text"`
	MeanTimeMs   float64   `json:"mean_time_ms"`
	TotalTimeMs  float64   `json:"total_time_ms"`
	Calls        int       `json:"calls"`
	RowsAvg      float64   `json:"rows_avg"`
}

// Store defines the monitoring data storage operations.
type Store interface {
	RecordHealthCheck(h HealthCheck) error
	RecordDBMetric(m DBMetric) error
	RecordPerformanceMetric(p PerformanceMetric) error

	QueryHealthChecks(connectionID string, since, until time.Time, limit int) ([]HealthCheck, error)
	QueryDBMetrics(connectionID string, since, until time.Time, limit int) ([]DBMetric, error)
	QueryPerformanceMetrics(connectionID string, since, until time.Time, limit int) ([]PerformanceMetric, error)

	// P2 — Advanced Monitoring
	RecordAutovacuumInfo(a AutovacuumInfo) error
	RecordLockInfo(l LockInfo) error
	RecordReplicationLag(r ReplicationLag) error
	RecordTableMetric(t TableMetric) error

	QueryAutovacuumInfo(connectionID string, since, until time.Time, limit int) ([]AutovacuumInfo, error)
	QueryLockInfo(connectionID string, since, until time.Time, limit int) ([]LockInfo, error)
	QueryReplicationLag(connectionID string, since, until time.Time, limit int) ([]ReplicationLag, error)
	QueryTableMetrics(connectionID string, since, until time.Time, limit int) ([]TableMetric, error)
}

// PGStore implements Store using PostgreSQL/TimescaleDB.
type PGStore struct {
	db *sql.DB
}

func NewPGStore(db *sql.DB) *PGStore {
	return &PGStore{db: db}
}

func (s *PGStore) RecordHealthCheck(h HealthCheck) error {
	_, err := s.db.Exec(`INSERT INTO health_checks (time, connection_id, status, response_time_ms, active_connections, error_message)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		h.Time, h.ConnectionID, h.Status, h.ResponseTimeMs, h.ActiveConnections, nullableStr(h.ErrorMessage))
	return err
}

func (s *PGStore) RecordDBMetric(m DBMetric) error {
	_, err := s.db.Exec(`INSERT INTO db_metrics (time, connection_id, db_type, db_size_bytes, growth_bytes, cache_hit_ratio, qps, connections_total, max_connections, conn_usage_percent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		m.Time, m.ConnectionID, m.DBType, m.DBSizeBytes, m.GrowthBytes, m.CacheHitRatio, m.QPS, m.ConnectionsTotal, m.MaxConnections, m.ConnUsagePercent)
	return err
}

func (s *PGStore) RecordPerformanceMetric(p PerformanceMetric) error {
	_, err := s.db.Exec(`INSERT INTO performance_metrics (time, connection_id, db_type, query_id, query_text, mean_time_ms, total_time_ms, calls, rows_avg)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.Time, p.ConnectionID, p.DBType, p.QueryID, p.QueryText, p.MeanTimeMs, p.TotalTimeMs, p.Calls, p.RowsAvg)
	return err
}

func (s *PGStore) QueryHealthChecks(connectionID string, since, until time.Time, limit int) ([]HealthCheck, error) {
	query := `SELECT time, connection_id, status, COALESCE(response_time_ms, 0), COALESCE(active_connections, 0), COALESCE(error_message, '')
		FROM health_checks WHERE 1=1`
	var args []interface{}
	argIdx := 1

	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY time DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []HealthCheck
	for rows.Next() {
		var h HealthCheck
		if err := rows.Scan(&h.Time, &h.ConnectionID, &h.Status, &h.ResponseTimeMs, &h.ActiveConnections, &h.ErrorMessage); err != nil {
			return nil, err
		}
		results = append(results, h)
	}
	return results, nil
}

func (s *PGStore) QueryDBMetrics(connectionID string, since, until time.Time, limit int) ([]DBMetric, error) {
	query := `SELECT time, connection_id, COALESCE(db_type, ''), COALESCE(db_size_bytes, 0), COALESCE(growth_bytes, 0),
		COALESCE(cache_hit_ratio, 0), COALESCE(qps, 0), COALESCE(connections_total, 0),
		COALESCE(max_connections, 0), COALESCE(conn_usage_percent, 0)
		FROM db_metrics WHERE 1=1`
	var args []interface{}
	argIdx := 1

	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY time DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DBMetric
	for rows.Next() {
		var m DBMetric
		if err := rows.Scan(&m.Time, &m.ConnectionID, &m.DBType, &m.DBSizeBytes, &m.GrowthBytes, &m.CacheHitRatio, &m.QPS, &m.ConnectionsTotal, &m.MaxConnections, &m.ConnUsagePercent); err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	return results, nil
}

func (s *PGStore) QueryPerformanceMetrics(connectionID string, since, until time.Time, limit int) ([]PerformanceMetric, error) {
	query := `SELECT time, connection_id, COALESCE(db_type, ''), COALESCE(query_id, ''), COALESCE(query_text, ''),
		COALESCE(mean_time_ms, 0), COALESCE(total_time_ms, 0), COALESCE(calls, 0), COALESCE(rows_avg, 0)
		FROM performance_metrics WHERE 1=1`
	var args []interface{}
	argIdx := 1

	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY mean_time_ms DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []PerformanceMetric
	for rows.Next() {
		var p PerformanceMetric
		if err := rows.Scan(&p.Time, &p.ConnectionID, &p.DBType, &p.QueryID, &p.QueryText, &p.MeanTimeMs, &p.TotalTimeMs, &p.Calls, &p.RowsAvg); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, nil
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ── P2 Store Implementations ──

func (s *PGStore) RecordAutovacuumInfo(a AutovacuumInfo) error {
	var lastVac, lastAna interface{}
	if a.LastAutovacuum != nil {
		lastVac = *a.LastAutovacuum
	}
	if a.LastAutoanalyze != nil {
		lastAna = *a.LastAutoanalyze
	}
	_, err := s.db.Exec(`INSERT INTO autovacuum_info
		(time, connection_id, db_type, table_name, schema_name, table_size_bytes, index_size_bytes,
		 dead_tuples, live_tuples, dead_tuple_ratio, last_autovacuum, last_autoanalyze,
		 n_auto_vacuum, n_auto_analyze, vacuum_threshold, mod_since_last_vacuum,
		 engine, table_rows, data_free_bytes, table_collation)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		a.Time, a.ConnectionID, a.DBType, a.TableName, a.SchemaName, a.TableSize, a.IndexSize,
		a.DeadTuples, a.LiveTuples, a.DeadTupleRatio, lastVac, lastAna,
		a.NAutoVacuum, a.NAutoAnalyze, a.VacuumThreshold, a.ModSinceLastVacuum,
		a.Engine, a.TableRows, a.DataFree, a.TableCollation)
	return err
}

func (s *PGStore) RecordLockInfo(l LockInfo) error {
	_, err := s.db.Exec(`INSERT INTO lock_info
		(time, connection_id, db_type, database_name, relation_name, lock_type, lock_mode, granted,
		 blocked_pid, blocked_user, blocked_query, blocked_duration_seconds,
		 blocking_pid, blocking_user, blocking_query, is_deadlock)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		l.Time, l.ConnectionID, l.DBType, l.DatabaseName, l.RelationName, l.LockType, l.LockMode, l.Granted,
		l.BlockedPID, l.BlockedUser, l.BlockedQuery, l.BlockedDuration,
		l.BlockingPID, l.BlockingUser, l.BlockingQuery, l.IsDeadlock)
	return err
}

func (s *PGStore) RecordReplicationLag(r ReplicationLag) error {
	_, err := s.db.Exec(`INSERT INTO replication_lag
		(time, connection_id, db_type, application_name, client_addr, state, sync_state,
		 write_lag_seconds, flush_lag_seconds, replay_lag_seconds,
		 slave_io_state, slave_io_thread, slave_sql_thread,
		 read_master_log_pos, exec_master_log_pos, relay_master_log_file,
		 seconds_behind_master, last_errno, last_error)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		r.Time, r.ConnectionID, r.DBType, r.ApplicationName, r.ClientAddr, r.State, r.SyncState,
		r.WriteLag, r.FlushLag, r.ReplayLag,
		r.SlaveIOState, r.SlaveIOThread, r.SlaveSQLThread,
		r.ReadMasterLogPos, r.ExecMasterLogPos, r.RelayMasterLogFile,
		r.SecondsBehindMaster, r.LastErrno, r.LastError)
	return err
}

func (s *PGStore) RecordTableMetric(t TableMetric) error {
	_, err := s.db.Exec(`INSERT INTO table_metrics
		(time, connection_id, db_type, database_name, schema_name, table_name,
		 table_size_bytes, index_size_bytes, total_size_bytes, row_estimate, fill_factor,
		 dead_tuple_ratio, engine, "collation")
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		t.Time, t.ConnectionID, t.DBType, t.DatabaseName, t.SchemaName, t.TableName,
		t.TableSize, t.IndexSize, t.TotalSize, t.RowEstimate, t.FillFactor,
		t.DeadTupleRatio, t.Engine, t.Collation)
	return err
}

// ── P2 Query Implementations ──

func (s *PGStore) QueryAutovacuumInfo(connectionID string, since, until time.Time, limit int) ([]AutovacuumInfo, error) {
	query := `SELECT time, connection_id, COALESCE(db_type,''), COALESCE(table_name,''), COALESCE(schema_name,''),
		COALESCE(table_size_bytes,0), COALESCE(index_size_bytes,0),
		COALESCE(dead_tuples,0), COALESCE(live_tuples,0), COALESCE(dead_tuple_ratio,0),
		last_autovacuum, last_autoanalyze,
		COALESCE(n_auto_vacuum,0), COALESCE(n_auto_analyze,0),
		COALESCE(vacuum_threshold,0), COALESCE(mod_since_last_vacuum,0),
		COALESCE(engine,''), COALESCE(table_rows,0), COALESCE(data_free_bytes,0), COALESCE(table_collation,'')
		FROM autovacuum_info WHERE 1=1`
	var args []interface{}
	argIdx := 1
	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY time DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []AutovacuumInfo
	for rows.Next() {
		var a AutovacuumInfo
		if err := rows.Scan(&a.Time, &a.ConnectionID, &a.DBType, &a.TableName, &a.SchemaName,
			&a.TableSize, &a.IndexSize,
			&a.DeadTuples, &a.LiveTuples, &a.DeadTupleRatio,
			&a.LastAutovacuum, &a.LastAutoanalyze,
			&a.NAutoVacuum, &a.NAutoAnalyze,
			&a.VacuumThreshold, &a.ModSinceLastVacuum,
			&a.Engine, &a.TableRows, &a.DataFree, &a.TableCollation); err != nil {
			return nil, err
		}
		results = append(results, a)
	}
	return results, nil
}

func (s *PGStore) QueryLockInfo(connectionID string, since, until time.Time, limit int) ([]LockInfo, error) {
	query := `SELECT time, connection_id, COALESCE(db_type,''), COALESCE(database_name,''), COALESCE(relation_name,''),
		COALESCE(lock_type,''), COALESCE(lock_mode,''), COALESCE(granted,true),
		COALESCE(blocked_pid,0), COALESCE(blocked_user,''), COALESCE(blocked_query,''), COALESCE(blocked_duration_seconds,0),
		COALESCE(blocking_pid,0), COALESCE(blocking_user,''), COALESCE(blocking_query,''), COALESCE(is_deadlock,false)
		FROM lock_info WHERE 1=1`
	var args []interface{}
	argIdx := 1
	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY time DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []LockInfo
	for rows.Next() {
		var l LockInfo
		if err := rows.Scan(&l.Time, &l.ConnectionID, &l.DBType, &l.DatabaseName, &l.RelationName,
			&l.LockType, &l.LockMode, &l.Granted,
			&l.BlockedPID, &l.BlockedUser, &l.BlockedQuery, &l.BlockedDuration,
			&l.BlockingPID, &l.BlockingUser, &l.BlockingQuery, &l.IsDeadlock); err != nil {
			return nil, err
		}
		results = append(results, l)
	}
	return results, nil
}

func (s *PGStore) QueryReplicationLag(connectionID string, since, until time.Time, limit int) ([]ReplicationLag, error) {
	query := `SELECT time, connection_id, COALESCE(db_type,''), COALESCE(application_name,''), COALESCE(client_addr,''),
		COALESCE(state,''), COALESCE(sync_state,''),
		COALESCE(write_lag_seconds,0), COALESCE(flush_lag_seconds,0), COALESCE(replay_lag_seconds,0),
		COALESCE(slave_io_state,''), COALESCE(slave_io_thread,''), COALESCE(slave_sql_thread,''),
		COALESCE(read_master_log_pos,0), COALESCE(exec_master_log_pos,0), COALESCE(relay_master_log_file,''),
		COALESCE(seconds_behind_master,0), COALESCE(last_errno,0), COALESCE(last_error,'')
		FROM replication_lag WHERE 1=1`
	var args []interface{}
	argIdx := 1
	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY time DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ReplicationLag
	for rows.Next() {
		var r ReplicationLag
		if err := rows.Scan(&r.Time, &r.ConnectionID, &r.DBType, &r.ApplicationName, &r.ClientAddr,
			&r.State, &r.SyncState,
			&r.WriteLag, &r.FlushLag, &r.ReplayLag,
			&r.SlaveIOState, &r.SlaveIOThread, &r.SlaveSQLThread,
			&r.ReadMasterLogPos, &r.ExecMasterLogPos, &r.RelayMasterLogFile,
			&r.SecondsBehindMaster, &r.LastErrno, &r.LastError); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (s *PGStore) QueryTableMetrics(connectionID string, since, until time.Time, limit int) ([]TableMetric, error) {
	query := `SELECT time, connection_id, COALESCE(db_type,''), COALESCE(database_name,''), COALESCE(schema_name,''),
		COALESCE(table_name,''),
		COALESCE(table_size_bytes,0), COALESCE(index_size_bytes,0), COALESCE(total_size_bytes,0),
		COALESCE(row_estimate,0), COALESCE(fill_factor,0),
		COALESCE(dead_tuple_ratio,0), COALESCE(engine,''), COALESCE("collation",'')
		FROM table_metrics WHERE 1=1`
	var args []interface{}
	argIdx := 1
	if connectionID != "" {
		query += ` AND connection_id = $` + itoa(argIdx)
		args = append(args, connectionID)
		argIdx++
	}
	if !since.IsZero() {
		query += ` AND time >= $` + itoa(argIdx)
		args = append(args, since)
		argIdx++
	}
	if !until.IsZero() {
		query += ` AND time <= $` + itoa(argIdx)
		args = append(args, until)
		argIdx++
	}
	query += ` ORDER BY total_size_bytes DESC LIMIT $` + itoa(argIdx)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []TableMetric
	for rows.Next() {
		var t TableMetric
		if err := rows.Scan(&t.Time, &t.ConnectionID, &t.DBType, &t.DatabaseName, &t.SchemaName,
			&t.TableName,
			&t.TableSize, &t.IndexSize, &t.TotalSize,
			&t.RowEstimate, &t.FillFactor,
			&t.DeadTupleRatio, &t.Engine, &t.Collation); err != nil {
			return nil, err
		}
		results = append(results, t)
	}
	return results, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
