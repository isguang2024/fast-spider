package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	protocolv1 "github.com/isguang2024/fast-spider/internal/protocol/v1"
	_ "modernc.org/sqlite"
)

const auditRetention = 30 * 24 * time.Hour

var (
	ErrNotFound     = errors.New("not found")
	ErrUnauthorized = errors.New("unauthorized")
	ErrExpired      = errors.New("expired")
	ErrConsumed     = errors.New("consumed")
	ErrRevoked      = errors.New("revoked")
	ErrReplay       = errors.New("replay detected")
	ErrConflict     = errors.New("conflict")
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db *sql.DB
}

type MachineRecord struct {
	ID                     string
	OwnerID                string
	DisplayName            string
	Status                 string
	OS                     string
	Arch                   string
	NodeVersion            string
	CapabilityDigest       string
	LastSeenAt             *time.Time
	LastConnectionGeneration int64
	RevokedAt              *time.Time
	Revision               int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type DeviceIdentity struct {
	Machine      MachineRecord
	CredentialID string
	PublicKey    string
	Fingerprint  string
}

type DeviceSession struct {
	MachineID    string
	CredentialID string
	OwnerID      string
	DisplayName  string
	PublicKey    string
	OS           string
	Arch         string
	NodeVersion  string
}

type EnrollmentInput struct {
	TokenHash       string
	IdempotencyKey  string
	MachineID       string
	CredentialID    string
	OwnerID         string
	DisplayName     string
	OS              string
	Arch            string
	NodeVersion     string
	PublicKey       string
	Fingerprint     string
	Now             time.Time
}

type EnrollmentResult struct {
	MachineID    string
	CredentialID string
	OwnerID      string
	AlreadyDone  bool
}

type AuditEntry struct {
	ID         string
	OwnerID    string
	MachineID  string
	ActorType  string
	ActorID    string
	Action     string
	Result     string
	RemoteAddr string
	Detail     any
	CreatedAt  time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single Hub process is the sole writer in Phase 1. Keeping one SQLite
	// connection also guarantees connection-scoped PRAGMAs stay effective.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply %s: %w", pragma, err)
		}
	}
	st := &Store{db: db}
	if err := st.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.Atoi(versionText)
		if err != nil {
			return fmt.Errorf("invalid migration name %q: %w", entry.Name(), err)
		}
		raw, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(raw)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		err = s.db.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version = ?", version).Scan(&existing)
		switch {
		case err == nil:
			if existing != checksum {
				return fmt.Errorf("migration %d checksum mismatch", version)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("query migration %d: %w", version, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(raw)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("execute migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(?,?,?,?)", version, entry.Name(), checksum, time.Now().UTC().Unix()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

func (s *Store) HasOwner(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM owners").Scan(&count); err != nil {
		return false, fmt.Errorf("count owners: %w", err)
	}
	return count > 0, nil
}

func (s *Store) EnsureBootstrapToken(ctx context.Context, id, tokenHash string, now, expires time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var owners int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM owners").Scan(&owners); err != nil {
		return false, err
	}
	if owners > 0 {
		return false, tx.Commit()
	}
	// Before the first Owner exists, each Hub restart rotates the bootstrap
	// secret. This guarantees the plaintext file and the database digest can
	// never drift after a crash.
	if _, err := tx.ExecContext(ctx, "DELETE FROM bootstrap_tokens"); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO bootstrap_tokens(id, token_hash, created_at, expires_at) VALUES(?,?,?,?)", id, tokenHash, now.Unix(), expires.Unix()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) BootstrapOwner(ctx context.Context, bootstrapHash, ownerID, displayName, apiTokenID, apiTokenHash string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	var expires int64
	var consumed sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT id, expires_at, consumed_at FROM bootstrap_tokens WHERE token_hash = ?", bootstrapHash).Scan(&id, &expires, &consumed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnauthorized
		}
		return err
	}
	if consumed.Valid {
		return ErrConsumed
	}
	if expires <= now.Unix() {
		return ErrExpired
	}
	var owners int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM owners").Scan(&owners); err != nil {
		return err
	}
	if owners > 0 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO owners(id, display_name, created_at) VALUES(?,?,?)", ownerID, displayName, now.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO owner_api_tokens(id, owner_id, token_hash, created_at) VALUES(?,?,?,?)", apiTokenID, ownerID, apiTokenHash, now.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bootstrap_tokens SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL", now.Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateOwnerToken(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	var ownerID string
	var expires sql.NullInt64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT owner_id, expires_at, revoked_at FROM owner_api_tokens WHERE token_hash = ?", tokenHash).Scan(&ownerID, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	if revoked.Valid {
		return "", ErrRevoked
	}
	if expires.Valid && expires.Int64 <= now.Unix() {
		return "", ErrExpired
	}
	return ownerID, nil
}

func (s *Store) CreateEnrollmentToken(ctx context.Context, id, ownerID, tokenHash, expectedName, expectedOS string, now, expires time.Time, maxAttempts int) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO enrollment_tokens(
		id, owner_id, token_hash, created_at, expires_at, max_attempts, expected_name, expected_os
	) VALUES(?,?,?,?,?,?,?,?)`, id, ownerID, tokenHash, now.Unix(), expires.Unix(), maxAttempts, nullString(expectedName), nullString(expectedOS))
	return err
}

func (s *Store) Enroll(ctx context.Context, in EnrollmentInput) (EnrollmentResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer tx.Rollback()
	var tokenID, ownerID string
	var expires int64
	var maxAttempts, attempts int
	var consumed sql.NullInt64
	var expectedName, expectedOS, savedIdem, resultMachine sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, owner_id, expires_at, max_attempts, attempt_count, consumed_at,
		expected_name, expected_os, idempotency_key, result_machine_id
		FROM enrollment_tokens WHERE token_hash = ?`, in.TokenHash).
		Scan(&tokenID, &ownerID, &expires, &maxAttempts, &attempts, &consumed, &expectedName, &expectedOS, &savedIdem, &resultMachine)
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollmentResult{}, ErrUnauthorized
	}
	if err != nil {
		return EnrollmentResult{}, err
	}
	if consumed.Valid {
		if savedIdem.Valid && savedIdem.String == in.IdempotencyKey && resultMachine.Valid {
			var credentialID string
			if err := tx.QueryRowContext(ctx, "SELECT id FROM device_credentials WHERE machine_id = ? AND status = 'active' ORDER BY issued_at DESC LIMIT 1", resultMachine.String).Scan(&credentialID); err != nil {
				return EnrollmentResult{}, err
			}
			return EnrollmentResult{MachineID: resultMachine.String, CredentialID: credentialID, OwnerID: ownerID, AlreadyDone: true}, tx.Commit()
		}
		return EnrollmentResult{}, ErrConsumed
	}
	if expires <= in.Now.Unix() {
		return EnrollmentResult{}, ErrExpired
	}
	if attempts >= maxAttempts {
		return EnrollmentResult{}, ErrUnauthorized
	}
	if (expectedName.Valid && expectedName.String != in.DisplayName) || (expectedOS.Valid && expectedOS.String != in.OS) {
		attempts++
		_, _ = tx.ExecContext(ctx, "UPDATE enrollment_tokens SET attempt_count = ? WHERE id = ?", attempts, tokenID)
		if attempts >= maxAttempts {
			_, _ = tx.ExecContext(ctx, "UPDATE enrollment_tokens SET consumed_at = ? WHERE id = ?", in.Now.Unix(), tokenID)
		}
		if err := tx.Commit(); err != nil {
			return EnrollmentResult{}, err
		}
		return EnrollmentResult{}, ErrUnauthorized
	}
	if ownerID != in.OwnerID && in.OwnerID != "" {
		return EnrollmentResult{}, ErrUnauthorized
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO machines(
		id, owner_id, display_name, status, os, arch, node_version, created_at, updated_at
	) VALUES(?,?,?,'active',?,?,?,?,?)`, in.MachineID, ownerID, in.DisplayName, in.OS, in.Arch, in.NodeVersion, in.Now.Unix(), in.Now.Unix()); err != nil {
		return EnrollmentResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_credentials(
		id, machine_id, public_key, fingerprint, status, issued_at
	) VALUES(?,?,?,?, 'active', ?)`, in.CredentialID, in.MachineID, in.PublicKey, in.Fingerprint, in.Now.Unix()); err != nil {
		return EnrollmentResult{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE enrollment_tokens SET consumed_at = ?, idempotency_key = ?, result_machine_id = ?
		WHERE id = ? AND consumed_at IS NULL`, in.Now.Unix(), in.IdempotencyKey, in.MachineID, tokenID)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return EnrollmentResult{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentResult{}, err
	}
	return EnrollmentResult{MachineID: in.MachineID, CredentialID: in.CredentialID, OwnerID: ownerID}, nil
}

func (s *Store) GetDeviceIdentity(ctx context.Context, machineID string) (DeviceIdentity, error) {
	var rec MachineRecord
	var lastSeen, revoked sql.NullInt64
	var created, updated int64
	var credentialID, publicKey, fingerprint string
	err := s.db.QueryRowContext(ctx, `SELECT m.id, m.owner_id, m.display_name, m.status, m.os, m.arch, m.node_version,
		COALESCE(m.capability_digest,''), m.last_seen_at, m.last_connection_generation, m.revoked_at, m.revision,
		m.created_at, m.updated_at, c.id, c.public_key, c.fingerprint
		FROM machines m JOIN device_credentials c ON c.machine_id = m.id AND c.status = 'active'
		WHERE m.id = ? ORDER BY c.issued_at DESC LIMIT 1`, machineID).
		Scan(&rec.ID, &rec.OwnerID, &rec.DisplayName, &rec.Status, &rec.OS, &rec.Arch, &rec.NodeVersion,
			&rec.CapabilityDigest, &lastSeen, &rec.LastConnectionGeneration, &revoked, &rec.Revision,
			&created, &updated, &credentialID, &publicKey, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceIdentity{}, ErrNotFound
	}
	if err != nil {
		return DeviceIdentity{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.UpdatedAt = time.Unix(updated, 0).UTC()
	normalizeTimes(&rec, lastSeen, revoked)
	if rec.Status != "active" {
		return DeviceIdentity{}, ErrRevoked
	}
	return DeviceIdentity{Machine: rec, CredentialID: credentialID, PublicKey: publicKey, Fingerprint: fingerprint}, nil
}

func (s *Store) IssueDeviceToken(ctx context.Context, machineID, credentialID, nonceHash, tokenID, tokenHash string, now, expires time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM device_nonces WHERE expires_at <= ?", now.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO device_nonces(machine_id, nonce_hash, created_at, expires_at) VALUES(?,?,?,?)", machineID, nonceHash, now.Unix(), now.Add(10*time.Minute).Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrReplay
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_access_tokens(
		id, credential_id, machine_id, token_hash, issued_at, expires_at
	) VALUES(?,?,?,?,?,?)`, tokenID, credentialID, machineID, tokenHash, now.Unix(), expires.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AuthenticateDeviceToken(ctx context.Context, tokenHash string, now time.Time) (DeviceSession, error) {
	var session DeviceSession
	var expires int64
	var tokenRevoked, credentialRevoked sql.NullInt64
	var machineStatus, credentialStatus string
	err := s.db.QueryRowContext(ctx, `SELECT t.machine_id, t.credential_id, t.expires_at, t.revoked_at,
		m.owner_id, m.display_name, m.status, m.os, m.arch, m.node_version,
		c.public_key, c.status, c.revoked_at
		FROM device_access_tokens t
		JOIN machines m ON m.id = t.machine_id
		JOIN device_credentials c ON c.id = t.credential_id
		WHERE t.token_hash = ?`, tokenHash).
		Scan(&session.MachineID, &session.CredentialID, &expires, &tokenRevoked,
			&session.OwnerID, &session.DisplayName, &machineStatus, &session.OS, &session.Arch, &session.NodeVersion,
			&session.PublicKey, &credentialStatus, &credentialRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceSession{}, ErrUnauthorized
	}
	if err != nil {
		return DeviceSession{}, err
	}
	if tokenRevoked.Valid || credentialRevoked.Valid || machineStatus != "active" || credentialStatus != "active" {
		return DeviceSession{}, ErrRevoked
	}
	if expires <= now.Unix() {
		return DeviceSession{}, ErrExpired
	}
	return session, nil
}

func (s *Store) NextGeneration(ctx context.Context, machineID string, now time.Time) (int64, error) {
	var generation int64
	err := s.db.QueryRowContext(ctx, `UPDATE machines
		SET last_connection_generation = last_connection_generation + 1, last_seen_at = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND status = 'active'
		RETURNING last_connection_generation`, now.Unix(), now.Unix(), machineID).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrRevoked
	}
	return generation, err
}

func (s *Store) TouchMachine(ctx context.Context, machineID, osName, arch, nodeVersion, capabilityDigest string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE machines SET os = ?, arch = ?, node_version = ?, capability_digest = ?,
		last_seen_at = ?, updated_at = ? WHERE id = ? AND status = 'active'`, osName, arch, nodeVersion, capabilityDigest, now.Unix(), now.Unix(), machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRevoked
	}
	return nil
}

func (s *Store) ReplaceCapabilities(ctx context.Context, machineID string, capabilities []protocolv1.CapabilityDescriptor, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM machine_capabilities WHERE machine_id = ?", machineID); err != nil {
		return err
	}
	for _, capability := range capabilities {
		actions, err := json.Marshal(capability.Actions)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO machine_capabilities(machine_id, capability_id, version, actions_json, updated_at)
			VALUES(?,?,?,?,?)`, machineID, capability.CapabilityId, capability.Version, string(actions), now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListMachines(ctx context.Context, ownerID string) ([]MachineRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, display_name, status, os, arch, node_version,
		COALESCE(capability_digest,''), last_seen_at, last_connection_generation, revoked_at, revision, created_at, updated_at
		FROM machines WHERE owner_id = ? ORDER BY display_name, id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRecord
	for rows.Next() {
		var rec MachineRecord
		var lastSeen, revoked sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&rec.ID, &rec.OwnerID, &rec.DisplayName, &rec.Status, &rec.OS, &rec.Arch, &rec.NodeVersion,
			&rec.CapabilityDigest, &lastSeen, &rec.LastConnectionGeneration, &revoked, &rec.Revision, &created, &updated); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(created, 0).UTC()
		rec.UpdatedAt = time.Unix(updated, 0).UTC()
		normalizeTimes(&rec, lastSeen, revoked)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) GetMachine(ctx context.Context, ownerID, machineID string) (MachineRecord, error) {
	var rec MachineRecord
	var lastSeen, revoked sql.NullInt64
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, display_name, status, os, arch, node_version,
		COALESCE(capability_digest,''), last_seen_at, last_connection_generation, revoked_at, revision, created_at, updated_at
		FROM machines WHERE owner_id = ? AND id = ?`, ownerID, machineID).
		Scan(&rec.ID, &rec.OwnerID, &rec.DisplayName, &rec.Status, &rec.OS, &rec.Arch, &rec.NodeVersion,
			&rec.CapabilityDigest, &lastSeen, &rec.LastConnectionGeneration, &revoked, &rec.Revision, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRecord{}, ErrNotFound
	}
	if err != nil {
		return MachineRecord{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.UpdatedAt = time.Unix(updated, 0).UTC()
	normalizeTimes(&rec, lastSeen, revoked)
	return rec, nil
}

func (s *Store) Capabilities(ctx context.Context, machineID string) ([]protocolv1.CapabilityDescriptor, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT capability_id, version, actions_json FROM machine_capabilities WHERE machine_id = ? ORDER BY capability_id", machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocolv1.CapabilityDescriptor
	for rows.Next() {
		var item protocolv1.CapabilityDescriptor
		var actions string
		if err := rows.Scan(&item.CapabilityId, &item.Version, &actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(actions), &item.Actions); err != nil {
			return nil, fmt.Errorf("decode capability actions: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) RevokeMachine(ctx context.Context, ownerID, machineID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE machines SET status = 'revoked', revoked_at = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND owner_id = ? AND status <> 'revoked'`, now.Unix(), now.Unix(), machineID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM machines WHERE id = ? AND owner_id = ?", machineID, ownerID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE device_credentials SET status = 'revoked', revoked_at = ? WHERE machine_id = ? AND status <> 'revoked'", now.Unix(), machineID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE device_access_tokens SET revoked_at = ? WHERE machine_id = ? AND revoked_at IS NULL", now.Unix(), machineID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CleanupExpired(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DELETE FROM device_nonces WHERE expires_at <= ?",
		"DELETE FROM device_access_tokens WHERE expires_at <= ? OR revoked_at IS NOT NULL",
		"DELETE FROM enrollment_tokens WHERE expires_at <= ?",
		"DELETE FROM bootstrap_tokens WHERE expires_at <= ?",
	} {
		if _, err := tx.ExecContext(ctx, stmt, now.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_entries WHERE created_at <= ?", now.Add(-auditRetention).Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AppendAudit(ctx context.Context, entry AuditEntry) error {
	var detail any
	if entry.Detail != nil {
		raw, err := json.Marshal(entry.Detail)
		if err != nil {
			return err
		}
		detail = string(raw)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_entries(
		id, owner_id, machine_id, actor_type, actor_id, action, result, remote_addr, detail_json, created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?)`, entry.ID, nullString(entry.OwnerID), nullString(entry.MachineID), entry.ActorType,
		nullString(entry.ActorID), entry.Action, entry.Result, nullString(entry.RemoteAddr), detail, entry.CreatedAt.Unix())
	return err
}

func normalizeTimes(rec *MachineRecord, lastSeen, revoked sql.NullInt64) {
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		rec.LastSeenAt = &t
	}
	if revoked.Valid {
		t := time.Unix(revoked.Int64, 0).UTC()
		rec.RevokedAt = &t
	}
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
