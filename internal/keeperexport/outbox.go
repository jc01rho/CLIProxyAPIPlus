package keeperexport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var (
	ErrOutboxFull              = errors.New("keeper export outbox is full")
	ErrOutboxClosed            = errors.New("keeper export outbox is closed")
	ErrSequenceExhausted       = errors.New("keeper export outbox sequence is exhausted")
	ErrInstanceBindingMismatch = errors.New("keeper export outbox is bound to a different instance")
)

type OutboxEvent struct {
	Sequence  int64
	Payload   []byte
	CreatedAt time.Time
}

type OutboxStatus struct {
	StreamID            string
	InstanceID          string
	NextSequence        int64
	SequenceExhausted   bool
	AcknowledgedThrough int64
	BacklogEvents       int64
	BacklogBytes        int64
	OldestBacklogAt     *time.Time
	MetadataRevisions   map[MetadataCategory]int64
	LastError           string
}

type PreparedMetadata struct {
	Category MetadataCategory
	Revision int64
	Body     []byte
	Pending  bool
}

type outboxTestHooks struct {
	beforeAppendCommit func(context.Context) error
	beforeAckCommit    func(context.Context) error
}

type Outbox struct {
	mu        sync.Mutex
	db        *sql.DB
	path      string
	streamID  string
	maxBytes  int64
	closed    bool
	lastErr   string
	testHooks outboxTestHooks
}

func OpenOutbox(ctx context.Context, path string, maxBytes int64) (*Outbox, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("outbox path must be absolute")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("outbox max bytes must be positive")
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect outbox parent: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o222 == 0 {
		return nil, fmt.Errorf("outbox parent is not a writable directory")
	}
	if fileInfo, statErr := os.Stat(path); statErr == nil {
		if fileInfo.IsDir() || fileInfo.Mode().Perm()&0o222 == 0 {
			return nil, fmt.Errorf("outbox path is not a writable file")
		}
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect outbox file: %w", statErr)
	} else {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create restricted outbox: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("close new outbox: %w", closeErr)
		}
	}
	if err = os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("restrict outbox permissions: %w", err)
	}

	db, err := sql.Open("sqlite", sqliteFileDSN(path, runtime.GOOS == "windows"))
	if err != nil {
		return nil, fmt.Errorf("open outbox: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	outbox := &Outbox{db: db, path: path, maxBytes: maxBytes}
	if err = outbox.initialize(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err = outbox.restrictLiveFiles(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return outbox, nil
}

func sqliteFileDSN(path string, windows bool) string {
	uriPath := filepath.ToSlash(path)
	if windows && len(uriPath) >= 2 && uriPath[1] == ':' {
		// SQLite requires an absolute Windows drive path to have a leading
		// slash in a file URI: file:///C:/... rather than file:C:/....
		uriPath = "/" + uriPath
	}
	dsnURL := &url.URL{Scheme: "file", Path: uriPath}
	query := dsnURL.Query()
	query.Set("_busy_timeout", "250")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "FULL")
	dsnURL.RawQuery = query.Encode()
	return dsnURL.String()
}

func (o *Outbox) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS outbox_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS outbox_events (sequence INTEGER PRIMARY KEY, payload BLOB NOT NULL, payload_bytes INTEGER NOT NULL CHECK(payload_bytes >= 0), created_at_ms INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS outbox_metadata (category TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision >= 1), item_digest BLOB NOT NULL, request_body BLOB NOT NULL, pending INTEGER NOT NULL CHECK(pending IN (0,1))) WITHOUT ROWID`,
	} {
		if _, err := o.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize outbox schema: %w", err)
		}
	}
	var integrity string
	if err := o.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("check outbox integrity: %s: %w", integrity, err)
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin outbox initialization: %w", err)
	}
	defer tx.Rollback()
	streamID, err := metaValue(ctx, tx, "stream_id")
	if errors.Is(err, sql.ErrNoRows) {
		streamID, err = newUUIDv7()
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO outbox_meta(key,value) VALUES('stream_id',?)`, streamID)
		}
	}
	if err != nil {
		return fmt.Errorf("initialize stream ID: %w", err)
	}
	for key, value := range map[string]string{"next_sequence": "1", "sequence_exhausted": "0", "acknowledged_through": "0"} {
		if _, err = ensureMeta(ctx, tx, key, value); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit outbox initialization: %w", err)
	}
	o.streamID = streamID
	return nil
}

func ensureMeta(ctx context.Context, tx *sql.Tx, key, value string) (string, error) {
	current, err := metaValue(ctx, tx, key)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_meta(key,value) VALUES(?,?)`, key, value); err != nil {
			return "", fmt.Errorf("initialize outbox metadata %s: %w", key, err)
		}
		return value, nil
	}
	return current, err
}

func metaValue(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM outbox_meta WHERE key = ?`, key).Scan(&value)
	return value, err
}

// MetadataRevisionFloor returns the highest keeper-side metadata revision
// observed for the category (see SetMetadataRevisionFloor). Returns 0 when
// no floor has been recorded.
func (o *Outbox) MetadataRevisionFloor(ctx context.Context, category MetadataCategory) int64 {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0
	}
	value, err := metaValue(ctx, o.db, "metadata_floor_"+string(category))
	if err != nil {
		return 0
	}
	rev, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil || rev < 0 {
		return 0
	}
	return rev
}

// SetMetadataRevisionFloor records the highest keeper-side revision reported
// for a metadata category (keeper's current revision on stale/conflict, or
// the acknowledged revision after a successful apply). The floor only moves
// forward so a delayed report cannot walk it backwards.
func (o *Outbox) SetMetadataRevisionFloor(ctx context.Context, category MetadataCategory, revision int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if revision < 0 {
		revision = 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutboxClosed
	}
	value, err := metaValue(ctx, o.db, "metadata_floor_"+string(category))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read metadata revision floor: %w", err)
	}
	if current, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil && current >= revision {
		return nil
	}
	if _, err = o.db.ExecContext(ctx, `
		INSERT INTO outbox_meta(key,value) VALUES('metadata_floor_`+string(category)+`',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value
	`, strconv.FormatInt(revision, 10)); err != nil {
		return fmt.Errorf("write metadata revision floor: %w", err)
	}
	return nil
}

// DeleteMetadataUntilRevisionForTest removes row for the category so the next
// PrepareMetadata will start fresh. Used by tests that simulate an outbox DB
// reset while the durable floor remains.
func (o *Outbox) DeleteMetadataUntilRevisionForTest(category MetadataCategory) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutboxClosed
	}
	_, err := o.db.ExecContext(context.Background(),
		`DELETE FROM outbox_metadata WHERE category = ?`, category)
	if err != nil {
		return o.fail(err)
	}
	return nil
}

func newUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (o *Outbox) StreamID() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.streamID
}

func (o *Outbox) Binding(ctx context.Context) (string, []byte, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return "", nil, false, ErrOutboxClosed
	}
	instanceID, err := metaValue(ctx, o.db, "instance_id")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, o.fail(fmt.Errorf("read instance binding: %w", err))
	}
	secretHex, err := metaValue(ctx, o.db, "fingerprint_secret_hex")
	if err != nil {
		return "", nil, false, o.fail(fmt.Errorf("read fingerprint secret: %w", err))
	}
	secret, err := hex.DecodeString(secretHex)
	if err != nil || len(secret) != 32 {
		return "", nil, false, o.fail(fmt.Errorf("invalid persistent fingerprint secret"))
	}
	return instanceID, secret, true, nil
}

func (o *Outbox) BindInstance(ctx context.Context, instanceID string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !isUUIDv7(instanceID) {
		return nil, fmt.Errorf("invalid Keeper instance ID")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrOutboxClosed
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, o.fail(fmt.Errorf("begin instance binding: %w", err))
	}
	defer tx.Rollback()
	bound, err := metaValue(ctx, tx, "instance_id")
	if err == nil && bound != instanceID {
		return nil, o.fail(ErrInstanceBindingMismatch)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_meta(key,value) VALUES('instance_id',?)`, instanceID); err != nil {
			return nil, o.fail(fmt.Errorf("persist instance binding: %w", err))
		}
	} else if err != nil {
		return nil, o.fail(fmt.Errorf("read instance binding: %w", err))
	}
	secretHex, err := metaValue(ctx, tx, "fingerprint_secret_hex")
	if errors.Is(err, sql.ErrNoRows) {
		secret := make([]byte, 32)
		if _, err = rand.Read(secret); err != nil {
			return nil, o.fail(fmt.Errorf("generate fingerprint secret: %w", err))
		}
		secretHex = hex.EncodeToString(secret)
		if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_meta(key,value) VALUES('fingerprint_secret_hex',?)`, secretHex); err != nil {
			return nil, o.fail(fmt.Errorf("persist fingerprint secret: %w", err))
		}
	} else if err != nil {
		return nil, o.fail(fmt.Errorf("read fingerprint secret: %w", err))
	}
	secret, err := hex.DecodeString(secretHex)
	if err != nil || len(secret) != 32 {
		return nil, o.fail(fmt.Errorf("invalid persistent fingerprint secret"))
	}
	if err = tx.Commit(); err != nil {
		return nil, o.fail(fmt.Errorf("commit instance binding: %w", err))
	}
	if err = o.restrictLiveFiles(); err != nil {
		return nil, o.fail(err)
	}
	o.lastErr = ""
	return append([]byte(nil), secret...), nil
}

func (o *Outbox) Append(ctx context.Context, payload []byte) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(payload) == 0 {
		return 0, fmt.Errorf("outbox payload is empty")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0, ErrOutboxClosed
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, o.fail(fmt.Errorf("begin outbox append: %w", err))
	}
	defer tx.Rollback()
	var backlogBytes, sequence int64
	var exhausted bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(payload_bytes),0) FROM outbox_events`).Scan(&backlogBytes); err != nil {
		return 0, o.fail(fmt.Errorf("read outbox size: %w", err))
	}
	if int64(len(payload)) > o.maxBytes-backlogBytes {
		return 0, o.fail(ErrOutboxFull)
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM outbox_meta WHERE key='next_sequence'`).Scan(&sequence); err != nil {
		return 0, o.fail(fmt.Errorf("read next sequence: %w", err))
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) != 0 FROM outbox_meta WHERE key='sequence_exhausted'`).Scan(&exhausted); err != nil {
		return 0, o.fail(fmt.Errorf("read sequence exhaustion: %w", err))
	}
	if exhausted || sequence < 1 || sequence > MaxSafeInteger {
		return 0, o.fail(ErrSequenceExhausted)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox_events(sequence,payload,payload_bytes,created_at_ms) VALUES(?,?,?,?)`, sequence, append([]byte(nil), payload...), len(payload), time.Now().UTC().UnixMilli()); err != nil {
		return 0, o.fail(fmt.Errorf("insert outbox event: %w", err))
	}
	if sequence == MaxSafeInteger {
		_, err = tx.ExecContext(ctx, `UPDATE outbox_meta SET value='1' WHERE key='sequence_exhausted'`)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE outbox_meta SET value=? WHERE key='next_sequence'`, sequence+1)
	}
	if err != nil {
		return 0, o.fail(fmt.Errorf("advance outbox sequence: %w", err))
	}
	if hook := o.testHooks.beforeAppendCommit; hook != nil {
		if err = hook(ctx); err != nil {
			return 0, o.fail(err)
		}
	}
	if err = ctx.Err(); err != nil {
		return 0, o.fail(err)
	}
	if err = tx.Commit(); err != nil {
		return 0, o.fail(fmt.Errorf("commit outbox append: %w", err))
	}
	if err = o.restrictLiveFiles(); err != nil {
		return 0, o.fail(err)
	}
	o.lastErr = ""
	return sequence, nil
}

func (o *Outbox) List(ctx context.Context, maxEvents int, maxBytes int64) ([]OutboxEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxEvents <= 0 || maxBytes <= 0 {
		return nil, nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrOutboxClosed
	}
	rows, err := o.db.QueryContext(ctx, `SELECT sequence,payload,created_at_ms FROM outbox_events ORDER BY sequence LIMIT ?`, maxEvents)
	if err != nil {
		return nil, o.fail(fmt.Errorf("list outbox events: %w", err))
	}
	defer rows.Close()
	items := make([]OutboxEvent, 0, maxEvents)
	var used int64
	for rows.Next() {
		var event OutboxEvent
		var createdAtMs int64
		if err = rows.Scan(&event.Sequence, &event.Payload, &createdAtMs); err != nil {
			return nil, o.fail(fmt.Errorf("scan outbox event: %w", err))
		}
		if used+int64(len(event.Payload)) > maxBytes {
			break
		}
		event.Payload = append([]byte(nil), event.Payload...)
		event.CreatedAt = time.UnixMilli(createdAtMs).UTC()
		items = append(items, event)
		used += int64(len(event.Payload))
	}
	if err = rows.Err(); err != nil {
		return nil, o.fail(fmt.Errorf("iterate outbox events: %w", err))
	}
	return items, nil
}

func (o *Outbox) PrepareMetadata(ctx context.Context, category MetadataCategory, itemDigest, body []byte) (*PreparedMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(itemDigest) != 32 || len(body) == 0 || len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("invalid metadata snapshot preparation")
	}
	switch category {
	case CategoryAuthFiles, CategoryAPIKeys, CategoryProviderIdentities:
	default:
		return nil, fmt.Errorf("invalid metadata category")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrOutboxClosed
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, o.fail(fmt.Errorf("begin metadata preparation: %w", err))
	}
	defer tx.Rollback()
	var current PreparedMetadata
	var currentDigest []byte
	var pending int
	err = tx.QueryRowContext(ctx, `SELECT revision,item_digest,request_body,pending FROM outbox_metadata WHERE category=?`, category).
		Scan(&current.Revision, &currentDigest, &current.Body, &pending)
	if err == nil {
		current.Category = category
		current.Pending = pending != 0
		if string(currentDigest) == string(itemDigest) {
			return &current, nil
		}
		if current.Pending {
			return &current, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, o.fail(fmt.Errorf("read metadata preparation: %w", err))
	}
	revision := int64(1)
	if err == nil {
		if current.Revision >= MaxSafeInteger {
			return nil, o.fail(ErrSequenceExhausted)
		}
		revision = current.Revision + 1
	}
	// Resume above the highest keeper-side revision we have recorded (option 2
	// floor). This lets a fresh outbox row that was reset back to revision 1
	// continue above the durable floor instead of re-sending stale snapshots.
	// Read via tx: the outbox is pinned to a single SQLite connection, so
	// touching o.db while tx holds the connection would deadlock.
	if floor, ferr := metaValue(ctx, tx, "metadata_floor_"+string(category)); ferr == nil {
		if fv, perr := strconv.ParseInt(floor, 10, 64); perr == nil && fv+1 > revision {
			revision = fv + 1
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_metadata(category,revision,item_digest,request_body,pending) VALUES(?,?,?,?,1)
		ON CONFLICT(category) DO UPDATE SET revision=excluded.revision,item_digest=excluded.item_digest,request_body=excluded.request_body,pending=1`, category, revision, append([]byte(nil), itemDigest...), append([]byte(nil), body...))
	if err != nil {
		return nil, o.fail(fmt.Errorf("persist metadata preparation: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return nil, o.fail(fmt.Errorf("commit metadata preparation: %w", err))
	}
	return &PreparedMetadata{Category: category, Revision: revision, Body: append([]byte(nil), body...), Pending: true}, nil
}

// SupersedePendingMetadata replaces a locally pending snapshot after Keeper
// confirms that its matching revision was accepted with different content.
// Advancing the revision preserves the remote snapshot until the replacement
// has been durably acknowledged.
func (o *Outbox) SupersedePendingMetadata(ctx context.Context, category MetadataCategory, previousRevision int64, itemDigest, body []byte) (*PreparedMetadata, error) {
	return o.AdvancePendingMetadata(ctx, category, previousRevision, previousRevision+1, itemDigest, body)
}

// AdvancePendingMetadata supersedes a pending snapshot and jumps its revision
// to targetRevision in one step. It is used when Keeper reports a stale or
// conflicting revision and exposes its current revision, so the exporter can
// converge without walking one revision at a time.
func (o *Outbox) AdvancePendingMetadata(ctx context.Context, category MetadataCategory, previousRevision, targetRevision int64, itemDigest, body []byte) (*PreparedMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if previousRevision < 1 || targetRevision <= previousRevision || targetRevision >= MaxSafeInteger || len(itemDigest) != 32 || len(body) == 0 || len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("invalid metadata supersession")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrOutboxClosed
	}
	result, err := o.db.ExecContext(ctx, `UPDATE outbox_metadata
		SET revision=?,item_digest=?,request_body=?,pending=1
		WHERE category=? AND revision=? AND pending=1`,
		targetRevision, append([]byte(nil), itemDigest...), append([]byte(nil), body...), category, previousRevision)
	if err != nil {
		return nil, o.fail(fmt.Errorf("advance metadata preparation: %w", err))
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return nil, o.fail(fmt.Errorf("metadata supersession does not match pending revision"))
	}
	return &PreparedMetadata{Category: category, Revision: targetRevision, Body: append([]byte(nil), body...), Pending: true}, nil
}

func (o *Outbox) PendingMetadata(ctx context.Context, categories []MetadataCategory) ([]PreparedMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, ErrOutboxClosed
	}
	result := make([]PreparedMetadata, 0, len(categories))
	for _, category := range categories {
		var item PreparedMetadata
		var pending int
		err := o.db.QueryRowContext(ctx, `SELECT revision,request_body,pending FROM outbox_metadata WHERE category=?`, category).Scan(&item.Revision, &item.Body, &pending)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, o.fail(fmt.Errorf("read pending metadata: %w", err))
		}
		if pending != 0 {
			item.Category = category
			item.Pending = true
			item.Body = append([]byte(nil), item.Body...)
			result = append(result, item)
		}
	}
	return result, nil
}

func (o *Outbox) AcknowledgeMetadata(ctx context.Context, category MetadataCategory, revision int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutboxClosed
	}
	result, err := o.db.ExecContext(ctx, `UPDATE outbox_metadata SET pending=0 WHERE category=? AND revision=?`, category, revision)
	if err != nil {
		return o.fail(fmt.Errorf("acknowledge metadata: %w", err))
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return o.fail(fmt.Errorf("metadata ACK does not match prepared revision"))
	}
	return nil
}

func (o *Outbox) Acknowledge(ctx context.Context, through int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrOutboxClosed
	}
	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return o.fail(fmt.Errorf("begin outbox ACK: %w", err))
	}
	defer tx.Rollback()
	var acknowledged, next int64
	var exhausted bool
	if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM outbox_meta WHERE key='acknowledged_through'`).Scan(&acknowledged); err != nil {
		return o.fail(fmt.Errorf("read outbox ACK: %w", err))
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM outbox_meta WHERE key='next_sequence'`).Scan(&next); err != nil {
		return o.fail(fmt.Errorf("read next outbox sequence: %w", err))
	}
	if err = tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) != 0 FROM outbox_meta WHERE key='sequence_exhausted'`).Scan(&exhausted); err != nil {
		return o.fail(fmt.Errorf("read sequence exhaustion: %w", err))
	}
	maxAck := next - 1
	if exhausted {
		maxAck = MaxSafeInteger
	}
	if through < acknowledged || through > maxAck {
		return o.fail(fmt.Errorf("invalid ACK %d for acknowledged=%d max=%d", through, acknowledged, maxAck))
	}
	if through == acknowledged {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM outbox_events WHERE sequence <= ?`, through); err != nil {
		return o.fail(fmt.Errorf("compact acknowledged outbox events: %w", err))
	}
	if _, err = tx.ExecContext(ctx, `UPDATE outbox_meta SET value=? WHERE key='acknowledged_through'`, through); err != nil {
		return o.fail(fmt.Errorf("advance outbox ACK: %w", err))
	}
	if hook := o.testHooks.beforeAckCommit; hook != nil {
		if err = hook(ctx); err != nil {
			return o.fail(err)
		}
	}
	if err = ctx.Err(); err != nil {
		return o.fail(err)
	}
	if err = tx.Commit(); err != nil {
		return o.fail(fmt.Errorf("commit outbox ACK: %w", err))
	}
	if err = o.restrictLiveFiles(); err != nil {
		return o.fail(err)
	}
	o.lastErr = ""
	return nil
}

func (o *Outbox) Status(ctx context.Context) (OutboxStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return OutboxStatus{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return OutboxStatus{}, ErrOutboxClosed
	}
	status := OutboxStatus{StreamID: o.streamID, MetadataRevisions: make(map[MetadataCategory]int64), LastError: o.lastErr}
	var oldestMs sql.NullInt64
	err := o.db.QueryRowContext(ctx, `SELECT
		(SELECT CAST(value AS INTEGER) FROM outbox_meta WHERE key='next_sequence'),
		(SELECT CAST(value AS INTEGER) != 0 FROM outbox_meta WHERE key='sequence_exhausted'),
		(SELECT CAST(value AS INTEGER) FROM outbox_meta WHERE key='acknowledged_through'),
		COUNT(*), COALESCE(SUM(payload_bytes),0), MIN(created_at_ms)
		FROM outbox_events`).Scan(&status.NextSequence, &status.SequenceExhausted, &status.AcknowledgedThrough, &status.BacklogEvents, &status.BacklogBytes, &oldestMs)
	if err != nil {
		return OutboxStatus{}, o.fail(fmt.Errorf("read outbox status: %w", err))
	}
	if oldestMs.Valid {
		oldest := time.UnixMilli(oldestMs.Int64).UTC()
		status.OldestBacklogAt = &oldest
	}
	if instanceID, bindErr := metaValue(ctx, o.db, "instance_id"); bindErr == nil {
		status.InstanceID = instanceID
	} else if !errors.Is(bindErr, sql.ErrNoRows) {
		return OutboxStatus{}, o.fail(fmt.Errorf("read outbox instance: %w", bindErr))
	}
	rows, err := o.db.QueryContext(ctx, `SELECT category,revision FROM outbox_metadata`)
	if err != nil {
		return OutboxStatus{}, o.fail(fmt.Errorf("read metadata revisions: %w", err))
	}
	defer rows.Close()
	for rows.Next() {
		var category MetadataCategory
		var revision int64
		if err = rows.Scan(&category, &revision); err != nil {
			return OutboxStatus{}, o.fail(fmt.Errorf("scan metadata revision: %w", err))
		}
		status.MetadataRevisions[category] = revision
	}
	return status, rows.Err()
}

func (o *Outbox) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	return o.db.Close()
}

func (o *Outbox) restrictLiveFiles() error {
	for _, path := range []string{o.path, o.path + "-wal", o.path + "-shm", o.path + "-journal"} {
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restrict SQLite file %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func (o *Outbox) fail(err error) error {
	if err != nil {
		o.lastErr = err.Error()
	}
	return err
}
