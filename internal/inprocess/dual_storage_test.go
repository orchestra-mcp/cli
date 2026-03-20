package inprocess

import (
	"context"
	"errors"
	"testing"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
)

// fakeStorage is an in-memory StorageHandler for testing.
type fakeStorage struct {
	writeCalled  int
	deleteCalled int
	readCalled   int
	listCalled   int

	writeVersion  int64 // returned by Write
	writeErr      error
	deleteErr     error
	readErr       error
	listErr       error

	lastWriteExpectedVersion int64
	lastWritePath            string
	lastDeletePath           string
}

func (f *fakeStorage) Write(_ context.Context, req *pluginv1.StorageWriteRequest) (*pluginv1.StorageWriteResponse, error) {
	f.writeCalled++
	f.lastWriteExpectedVersion = req.ExpectedVersion
	f.lastWritePath = req.Path
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	return &pluginv1.StorageWriteResponse{Version: f.writeVersion}, nil
}

func (f *fakeStorage) Delete(_ context.Context, req *pluginv1.StorageDeleteRequest) (*pluginv1.StorageDeleteResponse, error) {
	f.deleteCalled++
	f.lastDeletePath = req.Path
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &pluginv1.StorageDeleteResponse{}, nil
}

func (f *fakeStorage) Read(_ context.Context, _ *pluginv1.StorageReadRequest) (*pluginv1.StorageReadResponse, error) {
	f.readCalled++
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &pluginv1.StorageReadResponse{}, nil
}

func (f *fakeStorage) List(_ context.Context, _ *pluginv1.StorageListRequest) (*pluginv1.StorageListResponse, error) {
	f.listCalled++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &pluginv1.StorageListResponse{}, nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestNewDualStorage_NotNil(t *testing.T) {
	primary := &fakeStorage{}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)
	if ds == nil {
		t.Fatal("NewDualStorage returned nil")
	}
	if ds.primary != primary {
		t.Error("primary not set correctly")
	}
	if ds.secondary != secondary {
		t.Error("secondary not set correctly")
	}
}

func TestDualStorageWrite_BothCalled(t *testing.T) {
	primary := &fakeStorage{writeVersion: 1}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	req := &pluginv1.StorageWriteRequest{Path: "project/features/FEAT-001.md", ExpectedVersion: 0}
	resp, err := ds.Write(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Version != 1 {
		t.Errorf("expected version 1 from primary, got %d", resp.Version)
	}
	if primary.writeCalled != 1 {
		t.Errorf("primary.Write called %d times, want 1", primary.writeCalled)
	}
	if secondary.writeCalled != 1 {
		t.Errorf("secondary.Write called %d times, want 1", secondary.writeCalled)
	}
}

func TestDualStorageWrite_MirrorUsesUpsertVersion(t *testing.T) {
	primary := &fakeStorage{writeVersion: 5}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	// Original request has a CAS version of 3.
	req := &pluginv1.StorageWriteRequest{Path: "project/features/FEAT-002.md", ExpectedVersion: 3}
	_, err := ds.Write(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Primary must receive original CAS version.
	if primary.lastWriteExpectedVersion != 3 {
		t.Errorf("primary ExpectedVersion = %d, want 3", primary.lastWriteExpectedVersion)
	}
	// Mirror must always receive -1 (unconditional upsert).
	if secondary.lastWriteExpectedVersion != -1 {
		t.Errorf("secondary ExpectedVersion = %d, want -1", secondary.lastWriteExpectedVersion)
	}
	// Original request must not be mutated.
	if req.ExpectedVersion != 3 {
		t.Error("original request was mutated by DualStorage.Write")
	}
}

func TestDualStorageWrite_MirrorFailureNonFatal(t *testing.T) {
	primary := &fakeStorage{writeVersion: 2}
	secondary := &fakeStorage{writeErr: errors.New("disk full")}
	ds := NewDualStorage(primary, secondary)

	req := &pluginv1.StorageWriteRequest{Path: "project/features/FEAT-003.md"}
	resp, err := ds.Write(context.Background(), req)
	// Secondary failure must not surface as an error.
	if err != nil {
		t.Fatalf("expected no error despite mirror failure, got: %v", err)
	}
	if resp.Version != 2 {
		t.Errorf("expected primary version 2, got %d", resp.Version)
	}
}

func TestDualStorageWrite_PrimaryFailure(t *testing.T) {
	primary := &fakeStorage{writeErr: errors.New("primary failure")}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	req := &pluginv1.StorageWriteRequest{Path: "project/features/FEAT-004.md"}
	_, err := ds.Write(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when primary fails")
	}
	// Secondary must NOT be called when primary fails.
	if secondary.writeCalled != 0 {
		t.Errorf("secondary.Write called %d times after primary failure, want 0", secondary.writeCalled)
	}
}

func TestDualStorageDelete_BothCalled(t *testing.T) {
	primary := &fakeStorage{}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	req := &pluginv1.StorageDeleteRequest{Path: "project/features/FEAT-005.md"}
	_, err := ds.Delete(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary.deleteCalled != 1 {
		t.Errorf("primary.Delete called %d times, want 1", primary.deleteCalled)
	}
	if secondary.deleteCalled != 1 {
		t.Errorf("secondary.Delete called %d times, want 1", secondary.deleteCalled)
	}
}

func TestDualStorageDelete_MirrorFailureNonFatal(t *testing.T) {
	primary := &fakeStorage{}
	secondary := &fakeStorage{deleteErr: errors.New("file not found")}
	ds := NewDualStorage(primary, secondary)

	req := &pluginv1.StorageDeleteRequest{Path: "project/features/FEAT-006.md"}
	_, err := ds.Delete(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error despite mirror delete failure, got: %v", err)
	}
}

func TestDualStorageRead_PrimaryOnly(t *testing.T) {
	primary := &fakeStorage{}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	_, err := ds.Read(context.Background(), &pluginv1.StorageReadRequest{Path: "project/features/FEAT-007.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary.readCalled != 1 {
		t.Errorf("primary.Read called %d times, want 1", primary.readCalled)
	}
	if secondary.readCalled != 0 {
		t.Errorf("secondary.Read called %d times, want 0 (Read must not touch secondary)", secondary.readCalled)
	}
}

func TestDualStorageList_PrimaryOnly(t *testing.T) {
	primary := &fakeStorage{}
	secondary := &fakeStorage{}
	ds := NewDualStorage(primary, secondary)

	_, err := ds.List(context.Background(), &pluginv1.StorageListRequest{Prefix: "project/features/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary.listCalled != 1 {
		t.Errorf("primary.List called %d times, want 1", primary.listCalled)
	}
	if secondary.listCalled != 0 {
		t.Errorf("secondary.List called %d times, want 0 (List must not touch secondary)", secondary.listCalled)
	}
}
