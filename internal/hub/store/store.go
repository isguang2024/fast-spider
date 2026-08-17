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

const (
	auditRetention                     = 30 * 24 * time.Hour
	oauthAuthorizationHistoryRetention = 90 * 24 * time.Hour
	oauthClientOrphanRetention         = 30 * time.Minute
	oauthMaxClientsPerOwner            = 128
)

var (
	ErrNotFound      = errors.New("not found")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrExpired       = errors.New("expired")
	ErrConsumed      = errors.New("consumed")
	ErrRevoked       = errors.New("revoked")
	ErrReplay        = errors.New("replay detected")
	ErrConflict      = errors.New("conflict")
	ErrResourceLimit = errors.New("resource limit exceeded")
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db *sql.DB
}

type MachineRecord struct {
	ID                       string
	OwnerID                  string
	DisplayName              string
	AdminNote                string
	Status                   string
	OS                       string
	Arch                     string
	NodeVersion              string
	CapabilityDigest         string
	LastSeenAt               *time.Time
	LastConnectionGeneration int64
	RevokedAt                *time.Time
	Revision                 int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
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

type MachineRegistrationInput struct {
	MachineID    string
	CredentialID string
	OwnerID      string
	DisplayName  string
	OS           string
	Arch         string
	NodeVersion  string
	PublicKey    string
	Fingerprint  string
	Now          time.Time
}

type MachineRegistrationResult struct {
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

type ArtifactRecord struct {
	ID          string     `json:"artifactId"`
	OwnerID     string     `json:"-"`
	MachineID   string     `json:"machineId"`
	JobID       string     `json:"jobId,omitempty"`
	LogicalName string     `json:"logicalName"`
	ContentType string     `json:"contentType"`
	SizeBytes   int64      `json:"sizeBytes"`
	SHA256      string     `json:"sha256"`
	StorageKey  string     `json:"-"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	ExpiresAt   time.Time  `json:"expiresAt"`
}

type ArtifactUploadRecord struct {
	ID             string
	ArtifactID     string
	MachineID      string
	ExpectedSize   int64
	ExpectedSHA256 string
	ReceivedSize   int64
	Status         string
	ExpiresAt      time.Time
}

type ArtifactUsageRecord struct {
	ActiveUploads int
	MachineBytes  int64
	OwnerBytes    int64
}

type ArtifactQuota struct {
	MaxActiveUploads int
	MaxMachineBytes  int64
	MaxOwnerBytes    int64
}

type ArtifactFileDeletion struct {
	Kind     string
	PathKey  string
	Attempts int
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

func (s *Store) AuthenticateConnectionToken(ctx context.Context, tokenHash string, now time.Time) (string, error) {
	var tokenID, ownerID string
	var expires, revoked, lastUsed sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		"SELECT id, owner_id, expires_at, revoked_at, last_used_at FROM connection_tokens WHERE token_hash = ? AND deleted_at IS NULL",
		tokenHash,
	).Scan(&tokenID, &ownerID, &expires, &revoked, &lastUsed)
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
	if !lastUsed.Valid || now.Unix()-lastUsed.Int64 >= 300 {
		_, _ = s.db.ExecContext(ctx,
			"UPDATE connection_tokens SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL AND deleted_at IS NULL",
			now.Unix(), tokenID,
		)
	}
	return ownerID, nil
}

func (s *Store) RegisterMachine(ctx context.Context, in MachineRegistrationInput) (MachineRegistrationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MachineRegistrationResult{}, err
	}
	defer tx.Rollback()

	var existingMachineID, existingCredentialID string
	err = tx.QueryRowContext(ctx, `SELECT m.id, c.id
		FROM machines m
		JOIN device_credentials c ON c.machine_id = m.id AND c.status = 'active'
		WHERE m.owner_id = ? AND m.status = 'active' AND c.public_key = ?
		ORDER BY c.issued_at DESC LIMIT 1`, in.OwnerID, in.PublicKey).
		Scan(&existingMachineID, &existingCredentialID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return MachineRegistrationResult{}, err
		}
		return MachineRegistrationResult{
			MachineID: existingMachineID, CredentialID: existingCredentialID,
			OwnerID: in.OwnerID, AlreadyDone: true,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MachineRegistrationResult{}, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO machines(
		id, owner_id, display_name, status, os, arch, node_version, created_at, updated_at
	) VALUES(?,?,?,'active',?,?,?,?,?)`, in.MachineID, in.OwnerID, in.DisplayName, in.OS, in.Arch, in.NodeVersion, in.Now.Unix(), in.Now.Unix()); err != nil {
		return MachineRegistrationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_credentials(
		id, machine_id, public_key, fingerprint, status, issued_at
	) VALUES(?,?,?,?, 'active', ?)`, in.CredentialID, in.MachineID, in.PublicKey, in.Fingerprint, in.Now.Unix()); err != nil {
		return MachineRegistrationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MachineRegistrationResult{}, err
	}
	return MachineRegistrationResult{MachineID: in.MachineID, CredentialID: in.CredentialID, OwnerID: in.OwnerID}, nil
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

func (s *Store) TouchMachine(ctx context.Context, machineID, displayName, osName, arch, nodeVersion, capabilityDigest string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE machines SET display_name = CASE WHEN ? <> '' THEN ? ELSE display_name END,
		os = ?, arch = ?, node_version = ?, capability_digest = ?, last_seen_at = ?, updated_at = ?
		WHERE id = ? AND status = 'active' AND deleted_at IS NULL`, displayName, displayName, osName, arch, nodeVersion, capabilityDigest, now.Unix(), now.Unix(), machineID)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, display_name, COALESCE(admin_note,''), status, os, arch, node_version,
		COALESCE(capability_digest,''), last_seen_at, last_connection_generation, revoked_at, revision, created_at, updated_at
		FROM machines WHERE owner_id = ? AND deleted_at IS NULL ORDER BY COALESCE(NULLIF(admin_note,''), display_name), id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRecord
	for rows.Next() {
		var rec MachineRecord
		var lastSeen, revoked sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&rec.ID, &rec.OwnerID, &rec.DisplayName, &rec.AdminNote, &rec.Status, &rec.OS, &rec.Arch, &rec.NodeVersion,
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
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_id, display_name, COALESCE(admin_note,''), status, os, arch, node_version,
		COALESCE(capability_digest,''), last_seen_at, last_connection_generation, revoked_at, revision, created_at, updated_at
		FROM machines WHERE owner_id = ? AND id = ? AND deleted_at IS NULL`, ownerID, machineID).
		Scan(&rec.ID, &rec.OwnerID, &rec.DisplayName, &rec.AdminNote, &rec.Status, &rec.OS, &rec.Arch, &rec.NodeVersion,
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

func (s *Store) CapabilitiesByOwner(ctx context.Context, ownerID string) (map[string][]protocolv1.CapabilityDescriptor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		mc.machine_id, mc.capability_id, mc.version, mc.actions_json
	FROM machine_capabilities mc
	JOIN machines m ON m.id = mc.machine_id
	WHERE m.owner_id = ? AND m.deleted_at IS NULL
	ORDER BY mc.machine_id, mc.capability_id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]protocolv1.CapabilityDescriptor)
	for rows.Next() {
		var machineID string
		var item protocolv1.CapabilityDescriptor
		var actions string
		if err := rows.Scan(&machineID, &item.CapabilityId, &item.Version, &actions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(actions), &item.Actions); err != nil {
			return nil, fmt.Errorf("decode capability actions: %w", err)
		}
		out[machineID] = append(out[machineID], item)
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
		WHERE id = ? AND owner_id = ? AND status <> 'revoked' AND deleted_at IS NULL`, now.Unix(), now.Unix(), machineID, ownerID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM machines WHERE id = ? AND owner_id = ? AND deleted_at IS NULL", machineID, ownerID).Scan(&exists); err != nil {
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

func (s *Store) UpdateMachineAdminNote(ctx context.Context, ownerID, machineID, adminNote string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE machines SET admin_note = NULLIF(?, ''), updated_at = ?, revision = revision + 1
		WHERE id = ? AND owner_id = ? AND deleted_at IS NULL`, adminNote, now.Unix(), machineID, ownerID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteMachine(ctx context.Context, ownerID, machineID string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE machines SET deleted_at = ?, updated_at = ?, revision = revision + 1
		WHERE id = ? AND owner_id = ? AND status = 'revoked' AND deleted_at IS NULL`,
		now.Unix(), now.Unix(), machineID, ownerID,
	)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Store) FindResumableArtifactUpload(ctx context.Context, ownerID, machineID, jobID, logicalName, contentType string, sizeBytes int64, sha256 string, now time.Time) (ArtifactUploadRecord, bool, error) {
	var rec ArtifactUploadRecord
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.artifact_id, u.machine_id, u.expected_size, u.expected_sha256, u.received_size, u.status, u.expires_at
		FROM artifact_uploads u JOIN artifacts a ON a.id = u.artifact_id
		WHERE a.owner_id = ? AND a.machine_id = ? AND COALESCE(a.job_id,'') = ?
		AND a.logical_name = ? AND a.content_type = ? AND a.size_bytes = ? AND a.sha256 = ?
		AND a.status = 'uploading' AND u.status = 'active' AND u.expires_at > ?
		ORDER BY u.created_at DESC LIMIT 1`, ownerID, machineID, jobID, logicalName, contentType, sizeBytes, sha256, now.Unix()).
		Scan(&rec.ID, &rec.ArtifactID, &rec.MachineID, &rec.ExpectedSize, &rec.ExpectedSHA256, &rec.ReceivedSize, &rec.Status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactUploadRecord{}, false, nil
	}
	if err != nil {
		return ArtifactUploadRecord{}, false, err
	}
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	return rec, true, nil
}

func (s *Store) ArtifactUsage(ctx context.Context, ownerID, machineID string, now time.Time) (ArtifactUsageRecord, error) {
	var usage ArtifactUsageRecord
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(expected_size),0)
		FROM artifact_uploads WHERE machine_id = ? AND status = 'active' AND expires_at > ?`, machineID, now.Unix()).
		Scan(&usage.ActiveUploads, &usage.MachineBytes); err != nil {
		return ArtifactUsageRecord{}, err
	}
	var completedMachineBytes int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifacts
		WHERE machine_id = ? AND status = 'complete' AND expires_at > ?`, machineID, now.Unix()).Scan(&completedMachineBytes); err != nil {
		return ArtifactUsageRecord{}, err
	}
	usage.MachineBytes += completedMachineBytes
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(a.size_bytes),0) FROM artifacts a
		WHERE a.owner_id = ? AND ((a.status = 'complete' AND a.expires_at > ?)
		OR (a.status = 'uploading' AND EXISTS (SELECT 1 FROM artifact_uploads u WHERE u.artifact_id = a.id AND u.status = 'active' AND u.expires_at > ?)))`, ownerID, now.Unix(), now.Unix()).Scan(&usage.OwnerBytes); err != nil {
		return ArtifactUsageRecord{}, err
	}
	return usage, nil
}

func (s *Store) CreateArtifactUpload(ctx context.Context, artifact ArtifactRecord, upload ArtifactUploadRecord, quota ArtifactQuota) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerID, status string
	if err := tx.QueryRowContext(ctx, "SELECT owner_id, status FROM machines WHERE id = ?", artifact.MachineID).Scan(&ownerID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if ownerID != artifact.OwnerID || status != "active" || upload.MachineID != artifact.MachineID || upload.ArtifactID != artifact.ID {
		return ErrUnauthorized
	}
	var usage ArtifactUsageRecord
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(expected_size),0)
		FROM artifact_uploads WHERE machine_id = ? AND status = 'active' AND expires_at > ?`, artifact.MachineID, artifact.CreatedAt.Unix()).
		Scan(&usage.ActiveUploads, &usage.MachineBytes); err != nil {
		return err
	}
	var completedMachineBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM artifacts
		WHERE machine_id = ? AND status = 'complete' AND expires_at > ?`, artifact.MachineID, artifact.CreatedAt.Unix()).Scan(&completedMachineBytes); err != nil {
		return err
	}
	usage.MachineBytes += completedMachineBytes
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(a.size_bytes),0) FROM artifacts a
		WHERE a.owner_id = ? AND ((a.status = 'complete' AND a.expires_at > ?)
		OR (a.status = 'uploading' AND EXISTS (SELECT 1 FROM artifact_uploads u WHERE u.artifact_id = a.id AND u.status = 'active' AND u.expires_at > ?)))`, artifact.OwnerID, artifact.CreatedAt.Unix(), artifact.CreatedAt.Unix()).Scan(&usage.OwnerBytes); err != nil {
		return err
	}
	if usage.ActiveUploads >= quota.MaxActiveUploads || usage.MachineBytes+artifact.SizeBytes > quota.MaxMachineBytes || usage.OwnerBytes+artifact.SizeBytes > quota.MaxOwnerBytes {
		return ErrResourceLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifacts(
		id, owner_id, machine_id, job_id, logical_name, content_type, size_bytes, sha256, status, created_at, expires_at
	) VALUES(?,?,?,?,?,?,?,?,'uploading',?,?)`, artifact.ID, artifact.OwnerID, artifact.MachineID, nullString(artifact.JobID), artifact.LogicalName, artifact.ContentType, artifact.SizeBytes, artifact.SHA256, artifact.CreatedAt.Unix(), artifact.ExpiresAt.Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_uploads(
		id, artifact_id, machine_id, expected_size, expected_sha256, received_size, status, created_at, expires_at
	) VALUES(?,?,?,?,?,0,'active',?,?)`, upload.ID, upload.ArtifactID, upload.MachineID, upload.ExpectedSize, upload.ExpectedSHA256, artifact.CreatedAt.Unix(), upload.ExpiresAt.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetArtifactUploadState(ctx context.Context, machineID, uploadID string) (ArtifactUploadRecord, error) {
	var rec ArtifactUploadRecord
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT id, artifact_id, machine_id, expected_size, expected_sha256, received_size, status, expires_at
		FROM artifact_uploads WHERE id = ? AND machine_id = ?`, uploadID, machineID).
		Scan(&rec.ID, &rec.ArtifactID, &rec.MachineID, &rec.ExpectedSize, &rec.ExpectedSHA256, &rec.ReceivedSize, &rec.Status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactUploadRecord{}, ErrNotFound
	}
	if err != nil {
		return ArtifactUploadRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	return rec, nil
}

func (s *Store) GetArtifactUpload(ctx context.Context, machineID, uploadID string, now time.Time) (ArtifactUploadRecord, error) {
	rec, err := s.GetArtifactUploadState(ctx, machineID, uploadID)
	if err != nil {
		return ArtifactUploadRecord{}, err
	}
	if rec.Status != "active" {
		return ArtifactUploadRecord{}, ErrConflict
	}
	if !rec.ExpiresAt.After(now) {
		return ArtifactUploadRecord{}, ErrExpired
	}
	return rec, nil
}

func (s *Store) AdvanceArtifactUpload(ctx context.Context, machineID, uploadID string, expectedOffset, newOffset int64, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE artifact_uploads SET received_size = ?
		WHERE id = ? AND machine_id = ? AND status = 'active' AND received_size = ? AND expires_at > ?`, newOffset, uploadID, machineID, expectedOffset, now.Unix())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) CompleteArtifactUpload(ctx context.Context, machineID, uploadID, storageKey string, now time.Time) (ArtifactRecord, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtifactRecord{}, err
	}
	defer tx.Rollback()
	var artifactID string
	var expectedSize, receivedSize, expiresAt int64
	var expectedSHA, status string
	if err := tx.QueryRowContext(ctx, `SELECT artifact_id, expected_size, expected_sha256, received_size, status, expires_at
		FROM artifact_uploads WHERE id = ? AND machine_id = ?`, uploadID, machineID).
		Scan(&artifactID, &expectedSize, &expectedSHA, &receivedSize, &status, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactRecord{}, ErrNotFound
		}
		return ArtifactRecord{}, err
	}
	if status != "active" || expectedSize != receivedSize {
		return ArtifactRecord{}, ErrConflict
	}
	if expiresAt <= now.Unix() {
		return ArtifactRecord{}, ErrExpired
	}
	if _, err := tx.ExecContext(ctx, "UPDATE artifact_uploads SET status = 'complete' WHERE id = ?", uploadID); err != nil {
		return ArtifactRecord{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE artifacts SET status = 'complete', storage_key = ?, completed_at = ? WHERE id = ? AND machine_id = ? AND status = 'uploading'", storageKey, now.Unix(), artifactID, machineID)
	if err != nil {
		return ArtifactRecord{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ArtifactRecord{}, err
	} else if affected != 1 {
		return ArtifactRecord{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return ArtifactRecord{}, err
	}
	return s.GetArtifactByMachine(ctx, machineID, artifactID)
}

func (s *Store) AbortArtifactUpload(ctx context.Context, machineID, uploadID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var artifactID, status string
	if err := tx.QueryRowContext(ctx, "SELECT artifact_id, status FROM artifact_uploads WHERE id = ? AND machine_id = ?", uploadID, machineID).Scan(&artifactID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == "aborted" {
		return tx.Commit()
	}
	if status != "active" {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE artifact_uploads SET status = 'aborted' WHERE id = ? AND status = 'active'", uploadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE artifacts SET status = 'aborted' WHERE id = ? AND status = 'uploading'", artifactID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetArtifact(ctx context.Context, ownerID, artifactID string) (ArtifactRecord, error) {
	return s.getArtifact(ctx, "owner_id", ownerID, artifactID)
}

func (s *Store) GetArtifactByMachine(ctx context.Context, machineID, artifactID string) (ArtifactRecord, error) {
	return s.getArtifact(ctx, "machine_id", machineID, artifactID)
}

func (s *Store) getArtifact(ctx context.Context, scopeColumn, scopeID, artifactID string) (ArtifactRecord, error) {
	if scopeColumn != "owner_id" && scopeColumn != "machine_id" {
		return ArtifactRecord{}, ErrUnauthorized
	}
	query := `SELECT id, owner_id, machine_id, COALESCE(job_id,''), logical_name, content_type, size_bytes, sha256, COALESCE(storage_key,''), status, created_at, completed_at, expires_at
		FROM artifacts WHERE id = ? AND ` + scopeColumn + ` = ?`
	var rec ArtifactRecord
	var created, expires int64
	var completed sql.NullInt64
	if err := s.db.QueryRowContext(ctx, query, artifactID, scopeID).Scan(&rec.ID, &rec.OwnerID, &rec.MachineID, &rec.JobID, &rec.LogicalName, &rec.ContentType, &rec.SizeBytes, &rec.SHA256, &rec.StorageKey, &rec.Status, &created, &completed, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactRecord{}, ErrNotFound
		}
		return ArtifactRecord{}, err
	}
	rec.CreatedAt = time.Unix(created, 0).UTC()
	rec.ExpiresAt = time.Unix(expires, 0).UTC()
	if completed.Valid {
		t := time.Unix(completed.Int64, 0).UTC()
		rec.CompletedAt = &t
	}
	return rec, nil
}

func (s *Store) CleanupArtifacts(ctx context.Context, now time.Time, limit int) ([]ArtifactFileDeletion, error) {
	if limit <= 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT u.id, u.artifact_id, u.status, COALESCE(a.sha256,'')
		FROM artifact_uploads u LEFT JOIN artifacts a ON a.id = u.artifact_id
		WHERE u.expires_at <= ? OR u.status = 'aborted' ORDER BY u.expires_at, u.id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	var uploadIDs, allUploadIDs, candidateStorageKeys []string
	var staleArtifactIDs []string
	for rows.Next() {
		var id, artifactID, status, artifactSHA string
		if err := rows.Scan(&id, &artifactID, &status, &artifactSHA); err != nil {
			rows.Close()
			return nil, err
		}
		allUploadIDs = append(allUploadIDs, id)
		// A completed upload can still have a .part file if the process exited
		// after committing reused-blob metadata but before removing the part.
		uploadIDs = append(uploadIDs, id)
		if status != "complete" {
			staleArtifactIDs = append(staleArtifactIDs, artifactID)
			if storageKey, ok := artifactStorageKeyFromSHA256(artifactSHA); ok {
				// CompleteArtifactUpload moves the part file before committing its
				// metadata. Deriving the content-addressed key lets maintenance
				// recover a blob left by a process crash in that small window.
				candidateStorageKeys = append(candidateStorageKeys, storageKey)
			}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := enqueueArtifactFileDeletions(ctx, tx, "upload", uploadIDs, now); err != nil {
		return nil, err
	}
	if err := deleteByIDs(ctx, tx, "artifact_uploads", allUploadIDs); err != nil {
		return nil, err
	}
	if err := deleteArtifactsByIDs(ctx, tx, staleArtifactIDs, true); err != nil {
		return nil, err
	}

	artifactRows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(storage_key,'') FROM artifacts a
		WHERE (a.status = 'complete' AND a.expires_at <= ?)
		OR (a.status <> 'complete' AND NOT EXISTS (SELECT 1 FROM artifact_uploads u WHERE u.artifact_id = a.id))
		ORDER BY a.expires_at, a.id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	var artifactIDs []string
	for artifactRows.Next() {
		var id, storageKey string
		if err := artifactRows.Scan(&id, &storageKey); err != nil {
			artifactRows.Close()
			return nil, err
		}
		artifactIDs = append(artifactIDs, id)
		if storageKey != "" {
			candidateStorageKeys = append(candidateStorageKeys, storageKey)
		}
	}
	if err := artifactRows.Close(); err != nil {
		return nil, err
	}
	if err := deleteArtifactsByIDs(ctx, tx, artifactIDs, false); err != nil {
		return nil, err
	}
	orphanStorageKeys, err := filterUnreferencedStorageKeys(ctx, tx, candidateStorageKeys)
	if err != nil {
		return nil, err
	}
	if err := enqueueArtifactFileDeletions(ctx, tx, "blob", orphanStorageKeys, now); err != nil {
		return nil, err
	}

	deletions, err := dueArtifactFileDeletions(ctx, tx, now, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deletions, nil
}

func (s *Store) CompleteArtifactFileDeletions(ctx context.Context, deletions []ArtifactFileDeletion) error {
	if len(deletions) == 0 {
		return nil
	}
	query := "DELETE FROM artifact_file_deletions WHERE "
	args := make([]any, 0, len(deletions)*2)
	for i, deletion := range deletions {
		if i > 0 {
			query += " OR "
		}
		query += "(kind = ? AND path_key = ?)"
		args = append(args, deletion.Kind, deletion.PathKey)
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// ArtifactStorageKeyReferenced is used immediately before a queued blob
// deletion. A previous removal failure may have outlived the metadata that
// queued it, while a later upload can legitimately reuse the same blob.
func (s *Store) ArtifactStorageKeyReferenced(ctx context.Context, storageKey string) (bool, error) {
	var referenced int
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM artifacts WHERE storage_key = ?)", storageKey).Scan(&referenced); err != nil {
		return false, err
	}
	return referenced != 0, nil
}

func (s *Store) FailArtifactFileDeletion(ctx context.Context, deletion ArtifactFileDeletion, failure error, now time.Time) error {
	message := "artifact file deletion failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	delay := 30 * time.Second
	for i := 0; i < deletion.Attempts && delay < time.Hour; i++ {
		delay *= 2
		if delay > time.Hour {
			delay = time.Hour
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE artifact_file_deletions
		SET attempts = attempts + 1, last_error = ?, next_attempt_at = ?, updated_at = ?
		WHERE kind = ? AND path_key = ?`, message, now.Add(delay).Unix(), now.Unix(), deletion.Kind, deletion.PathKey)
	return err
}

func enqueueArtifactFileDeletions(ctx context.Context, tx *sql.Tx, kind string, keys []string, now time.Time) error {
	keys = uniqueStrings(keys)
	if len(keys) == 0 {
		return nil
	}
	query := `INSERT INTO artifact_file_deletions(kind, path_key, attempts, next_attempt_at, created_at, updated_at) VALUES `
	args := make([]any, 0, len(keys)*5)
	for i, key := range keys {
		if i > 0 {
			query += ","
		}
		query += "(?,?,0,?,?,?)"
		args = append(args, kind, key, now.Unix(), now.Unix(), now.Unix())
	}
	query += " ON CONFLICT(kind, path_key) DO NOTHING"
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

func dueArtifactFileDeletions(ctx context.Context, tx *sql.Tx, now time.Time, limit int) ([]ArtifactFileDeletion, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind, path_key, attempts FROM artifact_file_deletions
		WHERE next_attempt_at <= ? ORDER BY next_attempt_at, kind, path_key LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactFileDeletion
	for rows.Next() {
		var item ArtifactFileDeletion
		if err := rows.Scan(&item.Kind, &item.PathKey, &item.Attempts); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func deleteByIDs(ctx context.Context, tx *sql.Tx, table string, ids []string) error {
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil
	}
	if table != "artifact_uploads" {
		return ErrUnauthorized
	}
	args := stringsToAny(ids)
	_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id IN ("+placeholders(len(ids))+")", args...)
	return err
}

func deleteArtifactsByIDs(ctx context.Context, tx *sql.Tx, ids []string, nonCompleteOnly bool) error {
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil
	}
	query := "DELETE FROM artifacts WHERE id IN (" + placeholders(len(ids)) + ")"
	if nonCompleteOnly {
		query += " AND status <> 'complete'"
	}
	_, err := tx.ExecContext(ctx, query, stringsToAny(ids)...)
	return err
}

func filterUnreferencedStorageKeys(ctx context.Context, tx *sql.Tx, candidates []string) ([]string, error) {
	candidates = uniqueStrings(candidates)
	if len(candidates) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT storage_key FROM artifacts WHERE storage_key IN ("+placeholders(len(candidates))+")", stringsToAny(candidates)...)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]struct{}, len(candidates))
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		referenced[key] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	orphans := make([]string, 0, len(candidates))
	for _, key := range candidates {
		if _, ok := referenced[key]; !ok {
			orphans = append(orphans, key)
		}
	}
	return orphans, nil
}

func artifactStorageKeyFromSHA256(value string) (string, bool) {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") || strings.ToLower(value) != value {
		return "", false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	if _, err := hex.DecodeString(digest); err != nil {
		return "", false
	}
	return digest[:2] + "/" + digest[2:4] + "/" + digest, true
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
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
		"DELETE FROM bootstrap_tokens WHERE expires_at <= ?",
		"DELETE FROM oauth_access_tokens WHERE expires_at <= ?",
		"DELETE FROM oauth_refresh_tokens WHERE expires_at <= ?",
		"DELETE FROM web_sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL",
	} {
		if _, err := tx.ExecContext(ctx, stmt, now.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM oauth_authorizations WHERE (revoked_at IS NOT NULL AND revoked_at <= ?) OR expires_at <= ?",
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_clients
		WHERE created_at <= ?
		AND NOT EXISTS (SELECT 1 FROM oauth_authorizations a WHERE a.client_id = oauth_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM oauth_access_tokens t WHERE t.client_id = oauth_clients.client_id)
		AND NOT EXISTS (SELECT 1 FROM oauth_refresh_tokens t WHERE t.client_id = oauth_clients.client_id)`,
		now.Add(-oauthClientOrphanRetention).Unix(),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM connection_tokens WHERE (revoked_at IS NOT NULL AND revoked_at <= ?) OR (expires_at IS NOT NULL AND expires_at <= ?)",
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM direct_access_keys WHERE (revoked_at IS NOT NULL AND revoked_at <= ?) OR expires_at <= ?",
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
		now.Add(-oauthAuthorizationHistoryRetention).Unix(),
	); err != nil {
		return err
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
