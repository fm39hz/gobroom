// Package artifacts provides bounded, owner-scoped spool leases for large
// operation artifacts. References carry metadata; body bytes stay out of JSON
// envelopes and are streamed from private files on demand.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/fm39hz/gobroom/internal/extensions"
)

var (
	ErrClosed       = errors.New("artifact body store is closed")
	ErrBodyTooLarge = errors.New("artifact body exceeds configured limit")
	ErrStoreFull    = errors.New("artifact body store capacity is exhausted")
	ErrExpired      = errors.New("artifact body lease has expired or was released")
	ErrOwner        = errors.New("artifact body owner does not match lease owner")
)

type Limits struct {
	MaxBytes     int64
	MaxBodyBytes int64
	DefaultTTL   time.Duration
}

type Store struct {
	mu         sync.Mutex
	dir        string
	removeDir  bool
	limits     Limits
	entries    map[string]*entry
	used       int64
	reserved   int64
	activePuts int
	closed     bool
}

type storeContextKey struct{}
type accessContextKey struct{}

type Access struct {
	Store     *Store
	Catalog   *extensions.Snapshot
	Receiver  extensions.ArtifactOwner
	Recipient extensions.Ref
	Allowlist *BodyAllowlist
}

// BodyAllowlist narrows a valid owner/recipient artifact contract to the
// leases produced by one response scope.
type BodyAllowlist struct {
	mu     sync.RWMutex
	refs   map[string]extensions.ArtifactRef
	closed bool
}

func newBodyAllowlist() *BodyAllowlist {
	return &BodyAllowlist{refs: make(map[string]extensions.ArtifactRef)}
}

func (a *BodyAllowlist) add(artifact extensions.ArtifactRef) bool {
	if a == nil || artifact.Body == nil || artifact.Body.ID == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	a.refs[artifact.Body.ID] = artifact.Clone()
	return true
}

func (a *BodyAllowlist) allows(artifact extensions.ArtifactRef) bool {
	if a == nil || artifact.Body == nil || artifact.Body.ID == "" {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return false
	}
	allowed, ok := a.refs[artifact.Body.ID]
	return ok && reflect.DeepEqual(allowed, artifact)
}

func (a *BodyAllowlist) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	a.refs = nil
	a.mu.Unlock()
}

func WithStore(ctx context.Context, store *Store) context.Context {
	return context.WithValue(ctx, storeContextKey{}, store)
}

func FromContext(ctx context.Context) (*Store, bool) {
	if ctx == nil {
		return nil, false
	}
	store, ok := ctx.Value(storeContextKey{}).(*Store)
	return store, ok && store != nil
}

func WithAccess(ctx context.Context, access Access) context.Context {
	return context.WithValue(WithStore(ctx, access.Store), accessContextKey{}, access)
}

func OpenArtifactFromContext(ctx context.Context, artifact extensions.ArtifactRef) (*Lease, error) {
	if ctx == nil {
		return nil, ErrClosed
	}
	access, ok := ctx.Value(accessContextKey{}).(Access)
	if !ok || access.Store == nil || access.Catalog == nil {
		return nil, fmt.Errorf("artifact access scope is unavailable")
	}
	if access.Allowlist != nil && !access.Allowlist.allows(artifact) {
		return nil, fmt.Errorf("artifact body is outside the active response scope")
	}
	return access.Store.OpenArtifact(ctx, access.Catalog, artifact, access.Receiver, access.Recipient)
}

type entry struct {
	ref      extensions.BodyRef
	owner    extensions.ArtifactOwner
	path     string
	leases   int
	released bool
}

func NewStore(dir string, limits Limits) (*Store, error) {
	if limits.MaxBytes <= 0 || limits.MaxBodyBytes <= 0 || limits.MaxBodyBytes > limits.MaxBytes {
		return nil, fmt.Errorf("artifact limits require 0 < maxBodyBytes <= maxBytes")
	}
	if limits.DefaultTTL <= 0 {
		limits.DefaultTTL = time.Hour
	}
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "gobroom-artifacts-")
		if err != nil {
			return nil, fmt.Errorf("create artifact spool directory: %w", err)
		}
	} else {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create artifact spool parent: %w", err)
		}
		privateDir, err := os.MkdirTemp(dir, "gobroom-artifacts-")
		if err != nil {
			return nil, fmt.Errorf("create private artifact spool directory: %w", err)
		}
		dir = privateDir
	}
	return &Store{dir: dir, removeDir: true, limits: limits, entries: map[string]*entry{}}, nil
}

// Put streams body into a private spool file while reserving capacity in
// bounded chunks. maxBytes may narrow the store's body limit for a specific
// artifact contract; it cannot increase the configured global maximum.
func (s *Store) Put(ctx context.Context, owner extensions.ArtifactOwner, mediaType string, source io.Reader, maxBytes int64, ttl time.Duration, replayable bool) (extensions.BodyRef, error) {
	if s == nil || ctx == nil || source == nil || owner.Domain == "" || mediaType == "" {
		return extensions.BodyRef{}, fmt.Errorf("artifact store, owner, media type and source are required")
	}
	s.Prune(time.Now())
	if err := ctx.Err(); err != nil {
		return extensions.BodyRef{}, err
	}
	if maxBytes <= 0 || maxBytes > s.limits.MaxBodyBytes {
		maxBytes = s.limits.MaxBodyBytes
	}
	if ttl <= 0 {
		ttl = s.limits.DefaultTTL
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return extensions.BodyRef{}, ErrClosed
	}
	s.activePuts++
	s.mu.Unlock()
	defer s.finishPut()

	file, err := os.CreateTemp(s.dir, "body-*.spool")
	if err != nil {
		return extensions.BodyRef{}, fmt.Errorf("create artifact spool file: %w", err)
	}
	path := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(path)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return extensions.BodyRef{}, fmt.Errorf("secure artifact spool file: %w", err)
	}
	hasher := sha256.New()
	writer := &reservedWriter{ctx: ctx, store: s, file: file, hasher: hasher, maxBytes: maxBytes}
	if _, err := io.Copy(writer, source); err != nil {
		writer.releaseReservation()
		cleanup()
		return extensions.BodyRef{}, err
	}
	if err := ctx.Err(); err != nil {
		writer.releaseReservation()
		cleanup()
		return extensions.BodyRef{}, err
	}
	if err := file.Sync(); err != nil {
		writer.releaseReservation()
		cleanup()
		return extensions.BodyRef{}, fmt.Errorf("sync artifact spool: %w", err)
	}
	if err := file.Close(); err != nil {
		writer.releaseReservation()
		_ = os.Remove(path)
		return extensions.BodyRef{}, fmt.Errorf("close artifact spool: %w", err)
	}
	ref := extensions.BodyRef{ID: filepath.Base(path), MediaType: mediaType, SizeBytes: writer.written, SHA256: hex.EncodeToString(hasher.Sum(nil)), ExpiresAt: time.Now().Add(ttl), Replayable: replayable}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.reserved -= writer.written
		_ = os.Remove(path)
		return extensions.BodyRef{}, ErrClosed
	}
	s.reserved -= writer.written
	s.used += writer.written
	s.entries[ref.ID] = &entry{ref: ref, owner: cloneOwner(owner), path: path}
	writer.reserved = 0
	return ref, nil
}

func (s *Store) finishPut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activePuts--
	if s.closed && s.removeDir && s.activePuts == 0 && len(s.entries) == 0 {
		_ = os.Remove(s.dir)
	}
}

type reservedWriter struct {
	ctx      context.Context
	store    *Store
	file     *os.File
	hasher   io.Writer
	maxBytes int64
	written  int64
	reserved int64
}

func (w *reservedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.maxBytes-w.written {
		return 0, ErrBodyTooLarge
	}
	w.store.mu.Lock()
	if w.store.closed {
		w.store.mu.Unlock()
		return 0, ErrClosed
	}
	if w.store.used+w.store.reserved+int64(len(p)) > w.store.limits.MaxBytes {
		w.store.mu.Unlock()
		return 0, ErrStoreFull
	}
	w.store.reserved += int64(len(p))
	w.reserved += int64(len(p))
	w.store.mu.Unlock()
	n, err := w.file.Write(p)
	if n > 0 {
		_, _ = w.hasher.Write(p[:n])
		w.written += int64(n)
	}
	unused := int64(len(p) - n)
	if unused > 0 {
		w.store.mu.Lock()
		w.store.reserved -= unused
		w.reserved -= unused
		w.store.mu.Unlock()
	}
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (w *reservedWriter) releaseReservation() {
	if w.reserved == 0 {
		return
	}
	w.store.mu.Lock()
	w.store.reserved -= w.reserved
	w.reserved = 0
	w.store.mu.Unlock()
}

// Open validates both the opaque lease token and its exact owner before
// returning a cancellable reader. The returned bytes are immutable and the
// lease keeps the spool entry alive until Close.
func (s *Store) Open(ctx context.Context, owner extensions.ArtifactOwner, ref extensions.BodyRef) (*Lease, error) {
	if s == nil || ctx == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	item := s.entries[ref.ID]
	if item == nil || item.released || (!item.ref.ExpiresAt.IsZero() && !item.ref.ExpiresAt.After(time.Now())) {
		if item != nil && !item.released {
			item.released = true
			if item.leases == 0 {
				_ = s.deleteEntryLocked(item)
			}
		}
		return nil, ErrExpired
	}
	if !ownersEqual(item.owner, owner) {
		return nil, ErrOwner
	}
	if !bodyRefsEqual(item.ref, ref) {
		return nil, fmt.Errorf("body lease metadata does not match stored reference")
	}
	file, err := os.Open(item.path)
	if err != nil {
		return nil, fmt.Errorf("open artifact body: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != item.ref.SizeBytes {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("stat artifact body: %w", err)
		}
		return nil, fmt.Errorf("artifact body file does not match its bounded reference")
	}
	item.leases++
	return &Lease{ctx: ctx, file: file, store: s, id: item.ref.ID}, nil
}

// OpenArtifact validates catalog ownership/recipient policy before acquiring
// the owner-scoped body lease. This is the cross-module read API; raw Open is
// only the lower-level store operation for a known owner.
func (s *Store) OpenArtifact(ctx context.Context, catalog *extensions.Snapshot, artifact extensions.ArtifactRef, receiver extensions.ArtifactOwner, recipient extensions.Ref) (*Lease, error) {
	if catalog == nil {
		return nil, fmt.Errorf("artifact contract catalog is unavailable")
	}
	if err := catalog.ValidateArtifact(artifact, receiver, recipient, time.Now()); err != nil {
		return nil, err
	}
	if artifact.Body == nil {
		return nil, fmt.Errorf("artifact has no leased body")
	}
	return s.Open(ctx, artifact.Owner, *artifact.Body)
}

func (s *Store) Release(owner extensions.ArtifactOwner, ref extensions.BodyRef) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.entries[ref.ID]
	if item == nil {
		return nil
	}
	if !ownersEqual(item.owner, owner) {
		return ErrOwner
	}
	if !bodyRefsEqual(item.ref, ref) {
		return fmt.Errorf("body lease metadata does not match stored reference")
	}
	item.released = true
	if item.leases == 0 {
		return s.deleteEntryLocked(item)
	}
	return nil
}

func (s *Store) Prune(now time.Time) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, item := range s.entries {
		if !item.ref.ExpiresAt.IsZero() && !item.ref.ExpiresAt.After(now) {
			item.released = true
			if item.leases == 0 && s.deleteEntryLocked(item) == nil {
				removed++
			}
		}
	}
	return removed
}

func (s *Store) deleteEntryLocked(item *entry) error {
	if err := os.Remove(item.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.entries, item.ref.ID)
	s.used -= item.ref.SizeBytes
	return nil
}

// Close prevents new leases and removes all unleased body files. Active readers
// remain valid until their lease closes; their files are then reclaimed.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var firstErr error
	for _, item := range s.entries {
		item.released = true
		if item.leases == 0 {
			if err := s.deleteEntryLocked(item); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if s.removeDir && s.activePuts == 0 && len(s.entries) == 0 {
		if err := os.Remove(s.dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type Lease struct {
	ctx    context.Context
	file   *os.File
	store  *Store
	id     string
	closed bool
	mu     sync.Mutex
}

func (l *Lease) Read(p []byte) (int, error) {
	if l == nil {
		return 0, ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, ErrClosed
	}
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	return l.file.Read(p)
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	err := l.file.Close()
	l.mu.Unlock()
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	item := l.store.entries[l.id]
	if item != nil {
		item.leases--
		if item.leases == 0 && item.released {
			removeErr := l.store.deleteEntryLocked(item)
			if err == nil {
				err = removeErr
			}
		}
	}
	if l.store.closed && l.store.removeDir && l.store.activePuts == 0 && len(l.store.entries) == 0 {
		removeErr := os.Remove(l.store.dir)
		if err == nil && removeErr != nil {
			err = removeErr
		}
	}
	return err
}

func bodyRefsEqual(left, right extensions.BodyRef) bool {
	expiryEqual := left.ExpiresAt.IsZero() && right.ExpiresAt.IsZero() || left.ExpiresAt.Equal(right.ExpiresAt)
	return left.ID == right.ID && left.MediaType == right.MediaType && left.SizeBytes == right.SizeBytes && left.SHA256 == right.SHA256 && left.Replayable == right.Replayable && expiryEqual
}

func cloneOwner(owner extensions.ArtifactOwner) extensions.ArtifactOwner {
	if owner.IssuerRef != nil {
		issuer := *owner.IssuerRef
		owner.IssuerRef = &issuer
	}
	return owner
}

func ownersEqual(left, right extensions.ArtifactOwner) bool {
	if left.Domain != right.Domain || left.ProviderDefinitionID != right.ProviderDefinitionID || left.ConnectionID != right.ConnectionID || left.ModelIdentity != right.ModelIdentity || left.ClientContract != right.ClientContract || left.SessionID != right.SessionID {
		return false
	}
	if left.IssuerRef == nil || right.IssuerRef == nil {
		return left.IssuerRef == nil && right.IssuerRef == nil
	}
	return *left.IssuerRef == *right.IssuerRef
}
