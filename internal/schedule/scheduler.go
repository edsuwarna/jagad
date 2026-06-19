package schedule

import (
	"fmt"
	"sync"
	"time"

	"github.com/edsuwarna/jagad/internal/backup"
	"github.com/edsuwarna/jagad/internal/connection"
	"github.com/robfig/cron/v3"
)

// BackupRunner is an interface for the backup service to start backups and enforce retention.
type BackupRunner interface {
	StartBackup(connectionID, databaseID, backupType string, scheduleID *string, storageProviderID *string, notifTargetIDs []string, notifOnSuccess, notifOnFailure bool) (*backup.Backup, error)
	EnforceRetention(scheduleID string, retentionFull, retentionIncr int)
}

// Scheduler wraps robfig/cron to manage scheduled backup execution.
type Scheduler struct {
	cron      *cron.Cron
	repo      Repository
	connRepo  connection.Repository
	runner    BackupRunner
	entries   map[string]cron.EntryID // scheduleID -> cron.EntryID
	mu        sync.RWMutex
}

// NewScheduler creates a new cron scheduler.
func NewScheduler(repo Repository, connRepo connection.Repository, runner BackupRunner) *Scheduler {
	return &Scheduler{
		cron:    cron.New(cron.WithParser(cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow))),
		repo:    repo,
		connRepo: connRepo,
		runner:  runner,
		entries: make(map[string]cron.EntryID),
	}
}

// Start begins the cron scheduler and loads all active schedules.
func (s *Scheduler) Start() error {
	schedules, err := s.repo.List()
	if err != nil {
		return fmt.Errorf("list schedules: %w", err)
	}

	for i := range schedules {
		sch := &schedules[i]
		if sch.Enabled {
			if err := s.addJob(sch); err != nil {
				fmt.Printf("WARN: failed to schedule %s: %v\n", sch.ID, err)
			}
		}
	}

	s.cron.Start()
	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// AddJob adds a schedule to the cron engine.
func (s *Scheduler) AddJob(sch *Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove existing entry if any
	if entryID, ok := s.entries[sch.ID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, sch.ID)
	}

	if !sch.Enabled {
		return nil
	}

	return s.addJob(sch)
}

// RemoveJob removes a schedule from the cron engine.
func (s *Scheduler) RemoveJob(scheduleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[scheduleID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, scheduleID)
	}
}

func (s *Scheduler) addJob(sch *Schedule) error {
	schedule := sch // capture
	entryID, err := s.cron.AddFunc(schedule.CronExpr, func() {
		s.executeBackup(schedule)
	})
	if err != nil {
		return fmt.Errorf("add cron job: %w", err)
	}

	s.entries[sch.ID] = entryID
	return nil
}

func (s *Scheduler) executeBackup(sch *Schedule) {
	fmt.Printf("[scheduler] running backup for schedule %s (conn=%s, backup_all=%v, db_ids=%v, db=%s, storage=%s)\n",
		sch.ID, sch.ConnectionID, sch.BackupAll, sch.DatabaseIDs, sch.DatabaseID, sch.StorageProviderID)

	// Resolve which databases to backup
	dbIDs, err := s.resolveDatabaseIDs(sch)
	if err != nil {
		fmt.Printf("[scheduler] ERROR resolving databases for schedule %s: %v\n", sch.ID, err)
		return
	}

	for _, dbID := range dbIDs {
		fmt.Printf("[scheduler] starting backup for schedule %s, db=%s\n", sch.ID, dbID)
		if sch.StorageProviderID != "" {
			storageProvID := &sch.StorageProviderID
			_, err := s.runner.StartBackup(sch.ConnectionID, dbID, sch.BackupType, &sch.ID, storageProvID, sch.NotifTargetIDs, sch.NotifyOnSuccess, sch.NotifyOnFailure)
			if err != nil {
				fmt.Printf("[scheduler] ERROR running backup for schedule %s, db=%s: %v\n", sch.ID, dbID, err)
			}
		} else {
			_, err := s.runner.StartBackup(sch.ConnectionID, dbID, sch.BackupType, &sch.ID, nil, sch.NotifTargetIDs, sch.NotifyOnSuccess, sch.NotifyOnFailure)
			if err != nil {
				fmt.Printf("[scheduler] ERROR running backup for schedule %s, db=%s: %v\n", sch.ID, dbID, err)
			}
		}
	}

	// Enforce retention policy after backup
	fmt.Printf("[scheduler] enforcing retention for schedule %s (full=%d, incr=%d)\n",
		sch.ID, sch.RetentionFull, sch.RetentionIncr)
	s.runner.EnforceRetention(sch.ID, sch.RetentionFull, sch.RetentionIncr)
}

// resolveDatabaseIDs returns the list of database IDs to backup for a schedule.
// Priority: BackupAll (discover all) > DatabaseIDs (specific selection) > DatabaseID (legacy single).
func (s *Scheduler) resolveDatabaseIDs(sch *Schedule) ([]string, error) {
	if sch.BackupAll {
		// Discover all databases for this connection
		dbs, err := s.connRepo.ListDatabases(sch.ConnectionID)
		if err != nil {
			return nil, fmt.Errorf("list databases for backup_all: %w", err)
		}
		ids := make([]string, len(dbs))
		for i, db := range dbs {
			ids[i] = db.ID
		}
		return ids, nil
	}

	if len(sch.DatabaseIDs) > 0 {
		return sch.DatabaseIDs, nil
	}

	// Legacy: single database_id
	if sch.DatabaseID != "" {
		return []string{sch.DatabaseID}, nil
	}

	return nil, fmt.Errorf("no database target defined (backup_all=false, no database_ids, no database_id)")
}

// RunNow manually triggers a scheduled backup immediately.
func (s *Scheduler) RunNow(scheduleID string) error {
	sch, err := s.repo.GetByID(scheduleID)
	if err != nil {
		return err
	}
	if sch == nil {
		return fmt.Errorf("schedule not found")
	}

	go s.executeBackup(sch)
	return nil
}

// ActiveEntries returns the number of active cron entries.
func (s *Scheduler) ActiveEntries() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// GetNextRun returns the next scheduled run time for a given schedule.
func (s *Scheduler) GetNextRun(scheduleID string) *time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if entryID, ok := s.entries[scheduleID]; ok {
		entry := s.cron.Entry(entryID)
		if entry.Next.IsZero() {
			return nil
		}
		return &entry.Next
	}
	return nil
}
