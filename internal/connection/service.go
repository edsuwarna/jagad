// Package connection provides business logic for managing database connections.
package connection

import (
	"database/sql"
	"fmt"
	"strings"
)

// Service handles connection management and database discovery.
type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Create adds a new database connection and returns it with generated ID.
// Automatically detects server version and discovers databases after creation.
func (s *Service) Create(conn *Connection) error {
	// Default SSL mode
	if conn.SSLMode == "" {
		conn.SSLMode = "prefer"
	}

	// Validate
	if conn.Name == "" || conn.Host == "" || conn.Port == 0 {
		return fmt.Errorf("name, host, and port are required")
	}
	if conn.DBType != "postgresql" && conn.DBType != "mysql" && conn.DBType != "mariadb" {
		return fmt.Errorf("unsupported database type: %s", conn.DBType)
	}

	// Test connection before saving (basic validation)
	if err := testConnection(conn); err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	// Save connection first
	if err := s.repo.Create(conn); err != nil {
		return err
	}

	// Auto-detect server version
	version, err := FetchVersion(conn)
	if err == nil {
		_ = s.repo.UpdateVersion(conn.ID, version)
		conn.DBVersion = version
	}

	// Auto-discover databases with sizes
	dbs, err := discoverDatabases(conn)
	if err == nil {
		for i := range dbs {
			dbs[i].ConnectionID = conn.ID
			dbs[i].IsSelected = true
		}
		if len(dbs) > 0 {
			_ = s.repo.UpsertDatabases(dbs)
		}
		conn.DBCount = len(dbs)
	}

	return nil
}

// Update modifies an existing connection.
// Preserves existing password if an empty password is provided.
// Re-fetches the database version after saving.
func (s *Service) Update(conn *Connection) error {
	if conn.ID == "" {
		return fmt.Errorf("id is required")
	}

	// Preserve existing password if empty string is provided
	if conn.Password == "" {
		existing, err := s.repo.GetByID(conn.ID)
		if err != nil {
			return fmt.Errorf("get existing connection: %w", err)
		}
		if existing != nil {
			conn.Password = existing.Password
		}
	}

	if err := s.repo.Update(conn); err != nil {
		return err
	}

	// Re-fetch database version after update
	version, err := FetchVersion(conn)
	if err == nil {
		_ = s.repo.UpdateVersion(conn.ID, version)
		conn.DBVersion = version
	}

	return nil
}

// Delete removes a connection and all associated databases/schedules/backups (cascade).
func (s *Service) Delete(id string) error {
	return s.repo.Delete(id)
}

// Get retrieves a single connection by ID.
func (s *Service) Get(id string) (*Connection, error) {
	return s.repo.GetByID(id)
}

// List returns all connections.
func (s *Service) List() ([]Connection, error) {
	return s.repo.List()
}

// Discover runs database discovery on a connection and saves results.
func (s *Service) Discover(connectionID string) ([]ConnectionDatabase, error) {
	conn, err := s.repo.GetByID(connectionID)
	if err != nil {
		return nil, fmt.Errorf("get connection: %w", err)
	}
	if conn == nil {
		return nil, fmt.Errorf("connection not found: %s", connectionID)
	}

	names, err := discoverDatabases(conn)
	if err != nil {
		return nil, fmt.Errorf("discover databases: %w", err)
	}

	// Set connection_id and selected on discovered databases
	for i := range names {
		names[i].ConnectionID = connectionID
		names[i].IsSelected = true
	}

	if err := s.repo.UpsertDatabases(names); err != nil {
		return nil, fmt.Errorf("save discovered databases: %w", err)
	}

	return s.repo.ListDatabases(connectionID)
}

// ListDatabases returns all discovered databases for a connection.
func (s *Service) ListDatabases(connectionID string) ([]ConnectionDatabase, error) {
	return s.repo.ListDatabases(connectionID)
}

// UpdateDatabaseSelection toggles whether a database is included in backups.
func (s *Service) UpdateDatabaseSelection(id string, selected bool) error {
	return s.repo.UpdateDatabaseSelection(id, selected)
}

// TestConnection verifies that a connection works.
func TestConnection(conn *Connection) error {
	return testConnection(conn)
}

// UpdateVersion stores the detected database version for a connection.
func (s *Service) UpdateVersion(id, version string) error {
	return s.repo.UpdateVersion(id, version)
}

// FetchVersion connects to the target database and queries its version string.
func FetchVersion(conn *Connection) (string, error) {
	switch conn.DBType {
	case "postgresql":
		return fetchPostgreSQLVersion(conn)
	case "mysql", "mariadb":
		return fetchMySQLVersion(conn)
	default:
		return "", fmt.Errorf("unsupported database type: %s", conn.DBType)
	}
}

func discoverDatabases(conn *Connection) ([]ConnectionDatabase, error) {
	// Generate random password for connection test if not provided
	switch conn.DBType {
	case "postgresql":
		return discoverPostgreSQL(conn)
	case "mysql", "mariadb":
		return discoverMySQL(conn)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", conn.DBType)
	}
}

func testConnection(conn *Connection) error {
	// For MVP, just try to open a connection and ping
	switch conn.DBType {
	case "postgresql":
		return testPostgreSQL(conn)
	case "mysql", "mariadb":
		return testMySQL(conn)
	default:
		return fmt.Errorf("unsupported database type: %s", conn.DBType)
	}
}

func testPostgreSQL(conn *Connection) error {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s connect_timeout=5",
		conn.Host, conn.Port, conn.Username, conn.Password, conn.SSLMode)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

func testMySQL(conn *Connection) error {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?tls=%s&timeout=5s&charset=utf8mb4",
		conn.Username, conn.Password, conn.Host, conn.Port, mapMySQLTLS(conn.SSLMode))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Ping()
}

func discoverPostgreSQL(conn *Connection) ([]ConnectionDatabase, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s connect_timeout=5",
		conn.Host, conn.Port, conn.Username, conn.Password, conn.SSLMode)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT datname, pg_database_size(datname) FROM pg_database WHERE datistemplate = false ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dbs []ConnectionDatabase
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			return nil, err
		}
		dbs = append(dbs, ConnectionDatabase{DBName: name, SizeBytes: size})
	}
	return dbs, nil
}

func discoverMySQL(conn *Connection) ([]ConnectionDatabase, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?tls=%s&timeout=5s&charset=utf8mb4",
		conn.Username, conn.Password, conn.Host, conn.Port, mapMySQLTLS(conn.SSLMode))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT table_schema, SUM(data_length + index_length) FROM information_schema.tables WHERE table_schema NOT IN ('information_schema', 'performance_schema', 'mysql', 'sys') AND table_schema NOT LIKE '\_%' GROUP BY table_schema ORDER BY table_schema`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dbs []ConnectionDatabase
	for rows.Next() {
		var name string
		var size sql.NullInt64
		if err := rows.Scan(&name, &size); err != nil {
			return nil, err
		}
		s := int64(0)
		if size.Valid {
			s = size.Int64
		}
		dbs = append(dbs, ConnectionDatabase{DBName: name, SizeBytes: s})
	}
	// Fallback to SHOW DATABASES if the query returns nothing (permissions)
	if len(dbs) == 0 {
		rows2, err := db.Query(`SHOW DATABASES`)
		if err != nil {
			return nil, err
		}
		defer rows2.Close()
		for rows2.Next() {
			var name string
			if err := rows2.Scan(&name); err != nil {
				return nil, err
			}
			if name == "information_schema" || name == "performance_schema" || name == "mysql" || name == "sys" {
				continue
			}
			if strings.HasPrefix(name, "_") {
				continue
			}
			dbs = append(dbs, ConnectionDatabase{DBName: name})
		}
	}
	return dbs, nil
}

// mapMySQLTLS maps PostgreSQL-style SSL modes to MySQL driver tls parameter values.
func mapMySQLTLS(mode string) string {
	switch mode {
	case "disable", "allow":
		return "false"
	case "prefer", "require", "verify-ca", "verify-full":
		return "skip-verify"
	default:
		return "skip-verify"
	}
}

func fetchPostgreSQLVersion(conn *Connection) (string, error) {
	dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=%s connect_timeout=5",
		conn.Host, conn.Port, conn.Username, conn.Password, conn.SSLMode)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()

	var version string
	err = db.QueryRow(`SELECT version()`).Scan(&version)
	if err != nil {
		return "", err
	}
	return version, nil
}

func fetchMySQLVersion(conn *Connection) (string, error) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?tls=%s&timeout=5s&charset=utf8mb4",
		conn.Username, conn.Password, conn.Host, conn.Port, mapMySQLTLS(conn.SSLMode))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return "", err
	}
	defer db.Close()

	var version string
	err = db.QueryRow(`SELECT VERSION()`).Scan(&version)
	if err != nil {
		return "", err
	}
	return version, nil
}
