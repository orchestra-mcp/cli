package inprocess

import (
	"context"
	"log"

	pluginv1 "github.com/orchestra-mcp/gen-go/orchestra/plugin/v1"
	"github.com/orchestra-mcp/sdk-go/plugin"
	"google.golang.org/protobuf/proto"
)

// DualStorage implements plugin.StorageHandler by dispatching to two backends
// simultaneously. The primary backend is authoritative for reads and versioning.
// The secondary backend receives a best-effort synchronous mirror of every
// write and delete. If the mirror operation fails, a warning is logged but the
// operation still succeeds (the primary result is returned).
//
// Reads and list operations are served from the primary only.
// Mirror writes always use ExpectedVersion = -1 (upsert) to avoid CAS
// version conflicts, since the primary and secondary version counters diverge.
type DualStorage struct {
	primary   plugin.StorageHandler
	secondary plugin.StorageHandler
}

// NewDualStorage creates a DualStorage that reads from primary and mirrors
// writes to secondary.
func NewDualStorage(primary, secondary plugin.StorageHandler) *DualStorage {
	return &DualStorage{primary: primary, secondary: secondary}
}

// Read delegates entirely to the primary backend.
func (d *DualStorage) Read(ctx context.Context, req *pluginv1.StorageReadRequest) (*pluginv1.StorageReadResponse, error) {
	return d.primary.Read(ctx, req)
}

// List delegates entirely to the primary backend.
func (d *DualStorage) List(ctx context.Context, req *pluginv1.StorageListRequest) (*pluginv1.StorageListResponse, error) {
	return d.primary.List(ctx, req)
}

// Write writes to the primary backend first. On success, it mirrors the write
// to the secondary backend using ExpectedVersion = -1 (unconditional upsert).
// A secondary failure is logged but does not fail the overall operation.
func (d *DualStorage) Write(ctx context.Context, req *pluginv1.StorageWriteRequest) (*pluginv1.StorageWriteResponse, error) {
	resp, err := d.primary.Write(ctx, req)
	if err != nil {
		return nil, err
	}

	// Clone the request before mutating to avoid affecting the caller's copy.
	mirrorReq := proto.Clone(req).(*pluginv1.StorageWriteRequest)
	mirrorReq.ExpectedVersion = -1 // upsert — no CAS check on the mirror

	if _, err2 := d.secondary.Write(ctx, mirrorReq); err2 != nil {
		log.Printf("[dual-storage] mirror write failed for %q: %v", req.Path, err2)
	}

	return resp, nil
}

// Delete deletes from the primary backend first. On success, it mirrors the
// delete to the secondary backend. A secondary failure is logged but does not
// fail the overall operation.
func (d *DualStorage) Delete(ctx context.Context, req *pluginv1.StorageDeleteRequest) (*pluginv1.StorageDeleteResponse, error) {
	resp, err := d.primary.Delete(ctx, req)
	if err != nil {
		return nil, err
	}

	if _, err2 := d.secondary.Delete(ctx, req); err2 != nil {
		log.Printf("[dual-storage] mirror delete failed for %q: %v", req.Path, err2)
	}

	return resp, nil
}
