// Copyright 2023 Rivian Automotive, Inc.
// Licensed under the Apache License, Version 2.0 (the “License”);
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an “AS IS” BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package azureutils implements utilities used to interact with Azure Data Lake
// Storage Gen2 (ADLS Gen2 / hierarchical namespace).
package azureutils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/datalakeerror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/file"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azdatalake/filesystem"
)

var (
	// ErrNotFound is returned when a path does not exist.
	ErrNotFound error = errors.New("the path does not exist")
	// ErrAlreadyExists is returned when a path already exists.
	ErrAlreadyExists error = errors.New("the path already exists")
)

// ObjectProperties describes the metadata of a single path.
type ObjectProperties struct {
	// ContentLength is the size in bytes of the path.
	ContentLength int64
	// LastModified is the last modified time of the path.
	LastModified time.Time
}

// PathItem describes a single entry returned by a list operation.
type PathItem struct {
	// Name is the path relative to the filesystem (container) root.
	Name string
	// ContentLength is the size in bytes of the path.
	ContentLength int64
	// LastModified is the last modified time of the path.
	LastModified time.Time
	// IsDirectory reports whether the path is a directory.
	IsDirectory bool
}

// ListResult is the result of a list operation.
type ListResult struct {
	// Paths are the entries returned by the list operation.
	Paths []PathItem
	// NextMarker is the continuation token for the next page, if any.
	NextMarker string
}

// Client defines the subset of ADLS Gen2 operations used by the Azure object store.
// It is satisfied by RealClient and by the in-memory MockClient used in tests.
type Client interface {
	Upload(ctx context.Context, key string, data []byte) error
	Download(ctx context.Context, key string) ([]byte, error)
	DownloadRange(ctx context.Context, key string, offset int64, count int64) ([]byte, error)
	GetProperties(ctx context.Context, key string) (ObjectProperties, error)
	Delete(ctx context.Context, key string) error
	DeleteDirectory(ctx context.Context, key string) error
	List(ctx context.Context, prefix string, marker string) (ListResult, error)
	Rename(ctx context.Context, source string, destination string, overwrite bool) error
}

// RealClient is a Client backed by a real ADLS Gen2 filesystem (container).
type RealClient struct {
	fileSystem *filesystem.Client
}

// Compile time check that RealClient implements Client.
var _ Client = (*RealClient)(nil)

// NewClient creates a RealClient for the given storage account and filesystem
// (container) using the provided token credential.
func NewClient(account string, fileSystemName string, cred azcore.TokenCredential, opts *filesystem.ClientOptions) (*RealClient, error) {
	serviceURL := fmt.Sprintf("https://%s.dfs.core.windows.net/%s", account, fileSystemName)
	fsClient, err := filesystem.NewClient(serviceURL, cred, opts)
	if err != nil {
		return nil, err
	}
	return &RealClient{fileSystem: fsClient}, nil
}

// NewClientFromFileSystem wraps an existing ADLS Gen2 filesystem client.
func NewClientFromFileSystem(fsClient *filesystem.Client) *RealClient {
	return &RealClient{fileSystem: fsClient}
}

// mapError translates ADLS Gen2 service errors into this package's sentinel errors.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if datalakeerror.HasCode(err, datalakeerror.PathNotFound, datalakeerror.SourcePathNotFound) {
		return errors.Join(ErrNotFound, err)
	}
	if datalakeerror.HasCode(err, datalakeerror.PathAlreadyExists) {
		return errors.Join(ErrAlreadyExists, err)
	}
	return err
}

// Upload writes data to the path at key, creating or overwriting it.
func (c *RealClient) Upload(ctx context.Context, key string, data []byte) error {
	fileClient := c.fileSystem.NewFileClient(key)
	// UploadBuffer only appends and flushes; the file (and any missing parent
	// directories) must be created first on ADLS Gen2.
	if _, err := fileClient.Create(ctx, nil); err != nil {
		return mapError(err)
	}
	if len(data) == 0 {
		return nil
	}
	return mapError(fileClient.UploadBuffer(ctx, data, nil))
}

// Download returns the bytes stored at key.
func (c *RealClient) Download(ctx context.Context, key string) ([]byte, error) {
	resp, err := c.fileSystem.NewFileClient(key).DownloadStream(ctx, nil)
	if err != nil {
		return nil, mapError(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return io.ReadAll(resp.Body)
}

// DownloadRange returns count bytes stored at key starting at offset.
func (c *RealClient) DownloadRange(ctx context.Context, key string, offset int64, count int64) ([]byte, error) {
	resp, err := c.fileSystem.NewFileClient(key).DownloadStream(ctx, &file.DownloadStreamOptions{
		Range: &file.HTTPRange{Offset: offset, Count: count},
	})
	if err != nil {
		return nil, mapError(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	return io.ReadAll(resp.Body)
}

// GetProperties returns the metadata of the path at key.
func (c *RealClient) GetProperties(ctx context.Context, key string) (ObjectProperties, error) {
	resp, err := c.fileSystem.NewFileClient(key).GetProperties(ctx, nil)
	if err != nil {
		return ObjectProperties{}, mapError(err)
	}
	var props ObjectProperties
	if resp.ContentLength != nil {
		props.ContentLength = *resp.ContentLength
	}
	if resp.LastModified != nil {
		props.LastModified = *resp.LastModified
	}
	return props, nil
}

// Delete removes the file at key.
func (c *RealClient) Delete(ctx context.Context, key string) error {
	_, err := c.fileSystem.NewFileClient(key).Delete(ctx, nil)
	return mapError(err)
}

// DeleteDirectory recursively removes the directory at key.
func (c *RealClient) DeleteDirectory(ctx context.Context, key string) error {
	// The DFS API rejects directory paths with a trailing slash (InvalidUri).
	key = strings.TrimSuffix(key, "/")
	_, err := c.fileSystem.NewDirectoryClient(key).Delete(ctx, nil)
	return mapError(err)
}

// List returns a single page of paths under prefix, resuming from marker if set.
func (c *RealClient) List(ctx context.Context, prefix string, marker string) (ListResult, error) {
	opts := &filesystem.ListPathsOptions{}
	if prefix != "" {
		opts.Prefix = &prefix
	}
	if marker != "" {
		opts.Marker = &marker
	}
	pager := c.fileSystem.NewListPathsPager(true, opts)

	var result ListResult
	if !pager.More() {
		return result, nil
	}
	page, err := pager.NextPage(ctx)
	if err != nil {
		return ListResult{}, mapError(err)
	}
	for _, p := range page.Paths {
		if p == nil {
			continue
		}
		item := PathItem{}
		if p.Name != nil {
			item.Name = *p.Name
		}
		if p.ContentLength != nil {
			item.ContentLength = *p.ContentLength
		}
		if p.IsDirectory != nil {
			item.IsDirectory = *p.IsDirectory
		}
		if p.LastModified != nil {
			if t, perr := time.Parse(time.RFC1123, *p.LastModified); perr == nil {
				item.LastModified = t
			}
		}
		result.Paths = append(result.Paths, item)
	}
	if page.Continuation != nil {
		result.NextMarker = *page.Continuation
	}
	return result, nil
}

// Rename moves the path at source to destination. When overwrite is false, the
// operation fails with ErrAlreadyExists if destination already exists, using an
// atomic If-None-Match precondition.
func (c *RealClient) Rename(ctx context.Context, source string, destination string, overwrite bool) error {
	var opts *file.RenameOptions
	if !overwrite {
		etag := azcore.ETagAny
		opts = &file.RenameOptions{
			AccessConditions: &file.AccessConditions{
				ModifiedAccessConditions: &file.ModifiedAccessConditions{
					IfNoneMatch: &etag,
				},
			},
		}
	}
	_, err := c.fileSystem.NewFileClient(source).Rename(ctx, destination, opts)
	return mapError(err)
}
