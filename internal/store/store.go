package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrUsed     = errors.New("pair code already used")
	ErrExpired  = errors.New("pair code expired")
)

type Tenant struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Disabled  bool   `json:"disabled"`
	CreatedAt int64  `json:"createdAt"`
}

type Device struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Disabled  bool   `json:"disabled"`
	CreatedAt int64  `json:"createdAt"`
	LastSeen  *int64 `json:"lastSeen,omitempty"`
}

type Session struct {
	IDHash    []byte
	TenantID  string
	DeviceID  string
	Token     []byte
	CreatedAt int64
	LastSeen  int64
	ExpiresAt int64
	UA        string
}

type PushSub struct {
	ID        string
	TenantID  string
	Endpoint  string
	P256dh    string
	Auth      string
	CreatedAt int64
}

type Store struct {
	db *sql.DB
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "relay.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meta(
			key TEXT PRIMARY KEY,
			value BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS tenants(
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS devices(
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			key_hash BLOB NOT NULL UNIQUE,
			disabled INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			last_seen INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS devices_tenant ON devices(tenant_id)`,
		`CREATE TABLE IF NOT EXISTS sessions(
			id_hash BLOB PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
			token BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			ua TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_tenant ON sessions(tenant_id)`,
		`CREATE INDEX IF NOT EXISTS sessions_device ON sessions(device_id)`,
		`CREATE INDEX IF NOT EXISTS sessions_expires ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS pair_codes(
			code_hash BLOB PRIMARY KEY,
			device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
			expires_at INTEGER NOT NULL,
			used_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS pair_codes_expires ON pair_codes(expires_at)`,
		`CREATE TABLE IF NOT EXISTS push_subs(
			id TEXT PRIMARY KEY,
			tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			endpoint TEXT NOT NULL UNIQUE,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS push_subs_tenant ON push_subs(tenant_id)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateTenant(name string) (*Tenant, error) {
	id := "t_" + randHex(8)
	now := time.Now().Unix()
	if _, err := s.db.Exec(`INSERT INTO tenants(id,name,disabled,created_at) VALUES(?,?,0,?)`, id, name, now); err != nil {
		return nil, err
	}
	return &Tenant{ID: id, Name: name, CreatedAt: now}, nil
}

func (s *Store) GetTenant(id string) (*Tenant, error) {
	return s.scanTenant(`SELECT id,name,disabled,created_at FROM tenants WHERE id=?`, id)
}

func (s *Store) GetTenantByName(name string) (*Tenant, error) {
	return s.scanTenant(`SELECT id,name,disabled,created_at FROM tenants WHERE name=?`, name)
}

func (s *Store) scanTenant(q string, arg any) (*Tenant, error) {
	var t Tenant
	var disabled int
	err := s.db.QueryRow(q, arg).Scan(&t.ID, &t.Name, &disabled, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Disabled = disabled != 0
	return &t, nil
}

func (s *Store) ListTenants() ([]Tenant, error) {
	rows, err := s.db.Query(`SELECT id,name,disabled,created_at FROM tenants ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		var disabled int
		if err := rows.Scan(&t.ID, &t.Name, &disabled, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Disabled = disabled != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTenant(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	qs := []string{
		`DELETE FROM push_subs WHERE tenant_id=?`,
		`DELETE FROM sessions WHERE tenant_id=?`,
		`DELETE FROM pair_codes WHERE device_id IN (SELECT id FROM devices WHERE tenant_id=?)`,
		`DELETE FROM devices WHERE tenant_id=?`,
		`DELETE FROM tenants WHERE id=?`,
	}
	for _, q := range qs {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetTenantDisabled(id string, disabled bool) error {
	_, err := s.db.Exec(`UPDATE tenants SET disabled=? WHERE id=?`, boolInt(disabled), id)
	return err
}

func (s *Store) CreateDevice(tenantID, name string, keyHash []byte) (*Device, error) {
	id := "d_" + randHex(8)
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO devices(id,tenant_id,name,key_hash,disabled,created_at) VALUES(?,?,?,?,0,?)`,
		id, tenantID, name, keyHash, now)
	if err != nil {
		return nil, err
	}
	return &Device{ID: id, TenantID: tenantID, Name: name, CreatedAt: now}, nil
}

func (s *Store) GetDeviceByKeyHash(keyHash []byte) (*Device, *Tenant, error) {
	var d Device
	var t Tenant
	var dDisabled, tDisabled int
	var lastSeen sql.NullInt64
	err := s.db.QueryRow(`
		SELECT d.id,d.tenant_id,d.name,d.disabled,d.created_at,d.last_seen,
		       t.id,t.name,t.disabled,t.created_at
		FROM devices d JOIN tenants t ON t.id=d.tenant_id
		WHERE d.key_hash=?`, keyHash).
		Scan(&d.ID, &d.TenantID, &d.Name, &dDisabled, &d.CreatedAt, &lastSeen,
			&t.ID, &t.Name, &tDisabled, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	d.Disabled = dDisabled != 0
	d.LastSeen = nullInt(lastSeen)
	t.Disabled = tDisabled != 0
	return &d, &t, nil
}

func (s *Store) GetDevice(id string) (*Device, error) {
	var d Device
	var disabled int
	var lastSeen sql.NullInt64
	err := s.db.QueryRow(`SELECT id,tenant_id,name,disabled,created_at,last_seen FROM devices WHERE id=?`, id).
		Scan(&d.ID, &d.TenantID, &d.Name, &disabled, &d.CreatedAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Disabled = disabled != 0
	d.LastSeen = nullInt(lastSeen)
	return &d, nil
}

func (s *Store) ListDevices(tenantID string) ([]Device, error) {
	rows, err := s.db.Query(`SELECT id,tenant_id,name,disabled,created_at,last_seen FROM devices WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var disabled int
		var lastSeen sql.NullInt64
		if err := rows.Scan(&d.ID, &d.TenantID, &d.Name, &disabled, &d.CreatedAt, &lastSeen); err != nil {
			return nil, err
		}
		d.Disabled = disabled != 0
		d.LastSeen = nullInt(lastSeen)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) FindDevice(tenantID, name string) (*Device, error) {
	q := `SELECT id,tenant_id,name,disabled,created_at,last_seen FROM devices WHERE tenant_id=?`
	args := []any{tenantID}
	if name != "" {
		q += ` AND name=?`
		args = append(args, name)
	}
	q += ` ORDER BY created_at LIMIT 1`
	var d Device
	var disabled int
	var lastSeen sql.NullInt64
	err := s.db.QueryRow(q, args...).Scan(&d.ID, &d.TenantID, &d.Name, &disabled, &d.CreatedAt, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Disabled = disabled != 0
	d.LastSeen = nullInt(lastSeen)
	return &d, nil
}

func (s *Store) TouchDevice(id string) error {
	_, err := s.db.Exec(`UPDATE devices SET last_seen=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (s *Store) SetDeviceDisabled(id string, disabled bool) error {
	_, err := s.db.Exec(`UPDATE devices SET disabled=? WHERE id=?`, boolInt(disabled), id)
	return err
}

func (s *Store) SetDeviceKey(id string, keyHash []byte) error {
	_, err := s.db.Exec(`UPDATE devices SET key_hash=? WHERE id=?`, keyHash, id)
	return err
}

func (s *Store) CreateSession(sess Session) error {
	_, err := s.db.Exec(`INSERT INTO sessions(id_hash,tenant_id,device_id,token,created_at,last_seen,expires_at,ua)
		VALUES(?,?,?,?,?,?,?,?)`, sess.IDHash, sess.TenantID, sess.DeviceID, sess.Token,
		sess.CreatedAt, sess.LastSeen, sess.ExpiresAt, sess.UA)
	return err
}

func (s *Store) GetSession(idHash []byte) (*Session, error) {
	var x Session
	err := s.db.QueryRow(`SELECT id_hash,tenant_id,device_id,token,created_at,last_seen,expires_at,ua
		FROM sessions WHERE id_hash=?`, idHash).
		Scan(&x.IDHash, &x.TenantID, &x.DeviceID, &x.Token, &x.CreatedAt, &x.LastSeen, &x.ExpiresAt, &x.UA)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &x, nil
}

func (s *Store) TouchSession(idHash []byte, lastSeen, expiresAt int64) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_seen=?,expires_at=? WHERE id_hash=?`, lastSeen, expiresAt, idHash)
	return err
}

func (s *Store) UpdateSessionToken(idHash, token []byte) error {
	_, err := s.db.Exec(`UPDATE sessions SET token=? WHERE id_hash=?`, token, idHash)
	return err
}

func (s *Store) DeleteSession(idHash []byte) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id_hash=?`, idHash)
	return err
}

func (s *Store) DeleteTenantSessions(tenantID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE tenant_id=?`, tenantID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) CountSessions(tenantID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

func (s *Store) CreatePairCode(codeHash []byte, deviceID string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO pair_codes(code_hash,device_id,expires_at) VALUES(?,?,?)`,
		codeHash, deviceID, expiresAt)
	return err
}

func (s *Store) ConsumePairCode(codeHash []byte, now int64) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var deviceID string
	var expiresAt int64
	var usedAt sql.NullInt64
	err = tx.QueryRow(`SELECT device_id,expires_at,used_at FROM pair_codes WHERE code_hash=?`, codeHash).
		Scan(&deviceID, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if usedAt.Valid {
		return "", ErrUsed
	}
	if expiresAt <= now {
		return "", ErrExpired
	}
	res, err := tx.Exec(`UPDATE pair_codes SET used_at=? WHERE code_hash=? AND used_at IS NULL`, now, codeHash)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrUsed
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return deviceID, nil
}

func (s *Store) Cleanup(now int64) error {
	if _, err := s.db.Exec(`DELETE FROM pair_codes WHERE expires_at<=? OR used_at IS NOT NULL`, now); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at<=?`, now)
	return err
}

func (s *Store) AddPushSub(sub PushSub) error {
	if sub.ID == "" {
		sub.ID = "p_" + randHex(8)
	}
	if sub.CreatedAt == 0 {
		sub.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO push_subs(id,tenant_id,endpoint,p256dh,auth,created_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(endpoint) DO UPDATE SET tenant_id=excluded.tenant_id,p256dh=excluded.p256dh,auth=excluded.auth`,
		sub.ID, sub.TenantID, sub.Endpoint, sub.P256dh, sub.Auth, sub.CreatedAt)
	return err
}

func (s *Store) DeletePushSub(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE endpoint=?`, endpoint)
	return err
}

func (s *Store) ListPushSubs(tenantID string) ([]PushSub, error) {
	rows, err := s.db.Query(`SELECT id,tenant_id,endpoint,p256dh,auth,created_at FROM push_subs WHERE tenant_id=?`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var x PushSub
		if err := rows.Scan(&x.ID, &x.TenantID, &x.Endpoint, &x.P256dh, &x.Auth, &x.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) CountPushSubs(tenantID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM push_subs WHERE tenant_id=?`, tenantID).Scan(&n)
	return n, err
}

func (s *Store) SetMeta(key string, value []byte) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) GetMeta(key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullInt(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	x := v.Int64
	return &x
}
