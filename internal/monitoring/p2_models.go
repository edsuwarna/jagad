package monitoring

import "time"

// ── M15: Autovacuum / Optimizer Status ──

// AutovacuumInfo represents vacuum/optimizer status for a database connection.
// For PostgreSQL: pg_stat_user_tables — dead tuples, last vacuum, auto-vacuum count.
// For MySQL/MariaDB: information_schema.tables + OPTIMIZE TABLE status.
type AutovacuumInfo struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connection_id"`
	DBType       string    `json:"db_type"`

	// Common
	TableName   string `json:"table_name"`
	SchemaName  string `json:"schema_name,omitempty"`
	TableSize   int64  `json:"table_size_bytes"`   // table size in bytes
	IndexSize   int64  `json:"index_size_bytes"`   // index size in bytes

	// PostgreSQL-specific
	DeadTuples         int64      `json:"dead_tuples,omitempty"`
	LiveTuples         int64      `json:"live_tuples,omitempty"`
	DeadTupleRatio     float64    `json:"dead_tuple_ratio,omitempty"`      // dead / (dead + live) * 100
	LastAutovacuum     *time.Time `json:"last_autovacuum,omitempty"`
	LastAutoanalyze    *time.Time `json:"last_autoanalyze,omitempty"`
	NAutoVacuum        int64      `json:"n_auto_vacuum,omitempty"`
	NAutoAnalyze       int64      `json:"n_auto_analyze,omitempty"`
	VacuumThreshold    int64      `json:"vacuum_threshold,omitempty"`     // autovacuum_vacuum_threshold + autovacuum_vacuum_scale_factor * reltuples
	ModSinceLastVacuum int64      `json:"mod_since_last_vacuum,omitempty"` // n_tup_mod - n_tup_mod_prev

	// MySQL/MariaDB-specific
	Engine        string `json:"engine,omitempty"`         // InnoDB, MyISAM, etc.
	TableRows     int64  `json:"table_rows,omitempty"`
	DataFree      int64  `json:"data_free_bytes,omitempty"` // fragmentation
	TableCollation string `json:"table_collation,omitempty"`
}

// ── M16: Lock Detection ──

// LockInfo represents a currently detected lock or blocking query.
type LockInfo struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connection_id"`
	DBType       string    `json:"db_type"`

	DatabaseName  string `json:"database_name"`
	RelationName  string `json:"relation_name,omitempty"`  // table/relation being locked
	LockType      string `json:"lock_type"`                // AccessExclusiveLock, RowExclusive, etc.
	LockMode      string `json:"lock_mode"`                // granted, waiting
	Granted       bool   `json:"granted"`

	// Blocking info
	BlockedPID      int    `json:"blocked_pid"`
	BlockedUser     string `json:"blocked_user,omitempty"`
	BlockedQuery    string `json:"blocked_query,omitempty"`
	BlockedDuration float64 `json:"blocked_duration_seconds,omitempty"` // how long blocked

	BlockingPID     int    `json:"blocking_pid,omitempty"`
	BlockingUser    string `json:"blocking_user,omitempty"`
	BlockingQuery   string `json:"blocking_query,omitempty"`

	IsDeadlock      bool   `json:"is_deadlock,omitempty"`
}

// Summary counts for dashboard
type LockSummary struct {
	TotalLocks    int `json:"total_locks"`
	WaitingLocks  int `json:"waiting_locks"`
	GrantedLocks  int `json:"granted_locks"`
	Deadlocks     int `json:"deadlocks"`
}

// ── M17: Replication Lag ──

// ReplicationLag represents replication status for a source database.
type ReplicationLag struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connection_id"`
	DBType       string    `json:"db_type"`

	// PostgreSQL
	ApplicationName string  `json:"application_name,omitempty"`
	ClientAddr      string  `json:"client_addr,omitempty"`
	State           string  `json:"state,omitempty"`           // streaming, catchup, etc.
	SyncState       string  `json:"sync_state,omitempty"`      // sync, async, potential
	WriteLag        float64 `json:"write_lag_seconds,omitempty"`
	FlushLag        float64 `json:"flush_lag_seconds,omitempty"`
	ReplayLag       float64 `json:"replay_lag_seconds,omitempty"`

	// MySQL/MariaDB
	SlaveIOState        string `json:"slave_io_state,omitempty"`
	SlaveIOThread       string `json:"slave_io_thread,omitempty"`      // Yes/No/Connecting
	SlaveSQLThread      string `json:"slave_sql_thread,omitempty"`     // Yes/No
	ReadMasterLogPos    int64  `json:"read_master_log_pos,omitempty"`
	ExecMasterLogPos    int64  `json:"exec_master_log_pos,omitempty"`
	RelayMasterLogFile  string `json:"relay_master_log_file,omitempty"`
	SecondsBehindMaster int    `json:"seconds_behind_master,omitempty"`
	LastErrno           int    `json:"last_errno,omitempty"`
	LastError           string `json:"last_error,omitempty"`
}

// ── M20: Table-Level Metrics ──

// TableMetric represents size & row info for the top N largest tables in a database.
type TableMetric struct {
	Time         time.Time `json:"time"`
	ConnectionID string    `json:"connection_id"`
	DBType       string    `json:"db_type"`
	DatabaseName string    `json:"database_name"`

	// Table info
	SchemaName   string `json:"schema_name"`
	TableName    string `json:"table_name"`
	TableSize    int64  `json:"table_size_bytes"`
	IndexSize    int64  `json:"index_size_bytes"`
	TotalSize    int64  `json:"total_size_bytes"`
	RowEstimate  int64  `json:"row_estimate"` // estimated row count
	FillFactor   int    `json:"fill_factor,omitempty"`

	// PG-specific
	DeadTupleRatio float64 `json:"dead_tuple_ratio,omitempty"` // from pg_stat_user_tables

	// MySQL-specific
	Engine      string `json:"engine,omitempty"`
	Collation   string `json:"collation,omitempty"`
}
