// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// The tools package supports locating, parsing, and filtering Ubuntu tools metadata in simplestreams format.
// See http://launchpad.net/simplestreams and in particular the doc/README file in that project for more information
// about the file formats.
package tools

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"launchpad.net/juju-core/environs/simplestreams"
	"launchpad.net/juju-core/environs/storage"
	"launchpad.net/juju-core/errors"
	coretools "launchpad.net/juju-core/tools"
	"launchpad.net/juju-core/utils/set"
	"launchpad.net/juju-core/version"
)

func init() {
	simplestreams.RegisterStructTags(ToolsMetadata{})
}

const (
	ContentDownload = "content-download"
	MirrorContentId = "com.ubuntu.juju:released:tools"
)

// This needs to be a var so we can override it for testing.
var DefaultBaseURL = "https://juju.canonical.com/tools"

// ToolsConstraint defines criteria used to find a tools metadata record.
type ToolsConstraint struct {
	simplestreams.LookupParams
	Version      version.Number
	MajorVersion int
	MinorVersion int
	Released     bool
}

// NewVersionedToolsConstraint returns a ToolsConstraint for a tools with a specific version.
func NewVersionedToolsConstraint(vers string, params simplestreams.LookupParams) *ToolsConstraint {
	versNum := version.MustParse(vers)
	return &ToolsConstraint{LookupParams: params, Version: versNum}
}

// NewGeneralToolsConstraint returns a ToolsConstraint for tools with matching major/minor version numbers.
func NewGeneralToolsConstraint(majorVersion, minorVersion int, released bool, params simplestreams.LookupParams) *ToolsConstraint {
	return &ToolsConstraint{LookupParams: params, Version: version.Zero,
		MajorVersion: majorVersion, MinorVersion: minorVersion, Released: released}
}

// Ids generates a string array representing product ids formed similarly to an ISCSI qualified name (IQN).
func (tc *ToolsConstraint) Ids() ([]string, error) {
	var allIds []string
	for _, series := range tc.Series {
		version, err := simplestreams.SeriesVersion(series)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(tc.Arches))
		for i, arch := range tc.Arches {
			ids[i] = fmt.Sprintf("com.ubuntu.juju:%s:%s", version, arch)
		}
		allIds = append(allIds, ids...)
	}
	return allIds, nil
}

// ToolsMetadata holds information about a particular tools tarball.
type ToolsMetadata struct {
	Release  string `json:"release"`
	Version  string `json:"version"`
	Arch     string `json:"arch"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
	FullPath string `json:"-"`
	FileType string `json:"ftype"`
	SHA256   string `json:"sha256"`
}

func (t *ToolsMetadata) String() string {
	return fmt.Sprintf("%+v", *t)
}

// binary returns the tools metadata's binary version,
// which may be used for map lookup.
func (t *ToolsMetadata) binary() version.Binary {
	return version.Binary{
		Number: version.MustParse(t.Version),
		Series: t.Release,
		Arch:   t.Arch,
	}
}

func (t *ToolsMetadata) productId() (string, error) {
	seriesVersion, err := simplestreams.SeriesVersion(t.Release)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("com.ubuntu.juju:%s:%s", seriesVersion, t.Arch), nil
}

func excludeDefaultSource(sources []simplestreams.DataSource) []simplestreams.DataSource {
	var result []simplestreams.DataSource
	for _, source := range sources {
		url, _ := source.URL("")
		if !strings.HasPrefix(url, "https://juju.canonical.com/tools") {
			result = append(result, source)
		}
	}
	return result
}

// Fetch returns a list of tools for the specified cloud matching the constraint.
// The base URL locations are as specified - the first location which has a file is the one used.
// Signed data is preferred, but if there is no signed data available and onlySigned is false,
// then unsigned data is used.
func Fetch(sources []simplestreams.DataSource, indexPath string, cons *ToolsConstraint, onlySigned bool) ([]*ToolsMetadata, error) {

	// TODO (wallyworld): 2013-09-05 bug 1220965
	// Until the official tools repository is set up, we don't want to use it.
	sources = excludeDefaultSource(sources)

	params := simplestreams.ValueParams{
		DataType:        ContentDownload,
		FilterFunc:      appendMatchingTools,
		MirrorContentId: MirrorContentId,
		ValueTemplate:   ToolsMetadata{},
	}
	items, err := simplestreams.GetMetadata(sources, indexPath, cons, onlySigned, params)
	if err != nil {
		return nil, err
	}
	metadata := make([]*ToolsMetadata, len(items))
	for i, md := range items {
		metadata[i] = md.(*ToolsMetadata)
	}
	return metadata, nil
}

// appendMatchingTools updates matchingTools with tools metadata records from tools which belong to the
// specified series. If a tools record already exists in matchingTools, it is not overwritten.
func appendMatchingTools(source simplestreams.DataSource, matchingTools []interface{},
	tools map[string]interface{}, cons simplestreams.LookupConstraint) []interface{} {

	toolsMap := make(map[version.Binary]*ToolsMetadata, len(matchingTools))
	for _, val := range matchingTools {
		tm := val.(*ToolsMetadata)
		toolsMap[tm.binary()] = tm
	}
	for _, val := range tools {
		tm := val.(*ToolsMetadata)
		if !set.NewStrings(cons.Params().Series...).Contains(tm.Release) {
			continue
		}
		if toolsConstraint, ok := cons.(*ToolsConstraint); ok {
			tmNumber := version.MustParse(tm.Version)
			if toolsConstraint.Version == version.Zero {
				if toolsConstraint.Released && tmNumber.IsDev() {
					continue
				}
				if toolsConstraint.MajorVersion >= 0 && toolsConstraint.MajorVersion != tmNumber.Major {
					continue
				}
				if toolsConstraint.MinorVersion >= 0 && toolsConstraint.MinorVersion != tmNumber.Minor {
					continue
				}
			} else {
				if toolsConstraint.Version != tmNumber {
					continue
				}
			}
		}
		if _, ok := toolsMap[tm.binary()]; !ok {
			tm.FullPath, _ = source.URL(tm.Path)
			matchingTools = append(matchingTools, tm)
		}
	}
	return matchingTools
}

type MetadataFile struct {
	Path string
	Data []byte
}

// MetadataFromTools returns a tools metadata list derived from the
// given tools list. The size and sha256 will not be computed if
// missing.
func MetadataFromTools(toolsList coretools.List) []*ToolsMetadata {
	metadata := make([]*ToolsMetadata, len(toolsList))
	for i, t := range toolsList {
		path := fmt.Sprintf("releases/juju-%s-%s-%s.tgz", t.Version.Number, t.Version.Series, t.Version.Arch)
		metadata[i] = &ToolsMetadata{
			Release:  t.Version.Series,
			Version:  t.Version.Number.String(),
			Arch:     t.Version.Arch,
			FullPath: t.URL,
			Path:     path,
			FileType: "tar.gz",
			Size:     t.Size,
			SHA256:   t.SHA256,
		}
	}
	return metadata
}

// ResolveMetadata resolves incomplete metadata
// by fetching the tools from storage and computing
// the metadata locally.
func ResolveMetadata(stor storage.StorageReader, metadata []*ToolsMetadata) error {
	for _, md := range metadata {
		if md.Size == 0 {
			binary := md.binary()
			logger.Infof("Fetching tools to generate hash: %v", binary)
			var sha256hash hash.Hash
			size, sha256hash, err := fetchToolsHash(stor, binary)
			if err != nil {
				return err
			}
			md.Size = size
			md.SHA256 = fmt.Sprintf("%x", sha256hash.Sum(nil))
		}
	}
	return nil
}

// MergeMetadata merges the given tools metadata.
// If metadata for the same tools version exists in both lists,
// an entry with non-empty size/SHA256 takes precedence. If
// both entries have information, prefer the entry from
// "newMetadata".
func MergeMetadata(newMetadata, oldMetadata []*ToolsMetadata) []*ToolsMetadata {
	merged := make(map[version.Binary]*ToolsMetadata)
	for _, tm := range newMetadata {
		merged[tm.binary()] = tm
	}
	for _, tm := range oldMetadata {
		binary := tm.binary()
		if existing, ok := merged[binary]; !ok || existing.Size == 0 {
			merged[binary] = tm
		}
	}
	list := make([]*ToolsMetadata, 0, len(merged))
	for _, metadata := range merged {
		list = append(list, metadata)
	}
	return list
}

// ReadMetadata returns the tools metadata from the given storage.
func ReadMetadata(store storage.StorageReader) ([]*ToolsMetadata, error) {
	dataSource := storage.NewStorageSimpleStreamsDataSource(store, "tools")
	toolsConstraint, err := makeToolsConstraint(simplestreams.CloudSpec{}, -1, -1, coretools.Filter{})
	if err != nil {
		return nil, err
	}
	metadata, err := Fetch([]simplestreams.DataSource{dataSource}, simplestreams.DefaultIndexPath, toolsConstraint, false)
	if err != nil && !errors.IsNotFoundError(err) {
		return nil, err
	}
	return metadata, nil
}

// WriteMetadata writes the given tools metadata to the given storage.
func WriteMetadata(stor storage.Storage, metadata []*ToolsMetadata) error {
	index, products, err := MarshalToolsMetadataJSON(metadata, time.Now())
	if err != nil {
		return err
	}
	metadataInfo := []MetadataFile{
		{simplestreams.UnsignedIndex, index},
		{ProductMetadataPath, products},
	}
	for _, md := range metadataInfo {
		logger.Infof("Writing %s", "tools/"+md.Path)
		err = stor.Put("tools/"+md.Path, bytes.NewReader(md.Data), int64(len(md.Data)))
		if err != nil {
			return err
		}
	}
	return nil
}

type ResolveFlag bool

const (
	DontResolve ResolveFlag = false
	Resolve     ResolveFlag = true
)

// MergeAndWriteMetadata reads the existing metadata from storage (if any),
// and merges it with metadata generated from the given tools list.
// If resolve is true, incomplete metadata is resolved by fetching the tools
// from the target storage. Finally, the resulting metadata is written to
// storage.
func MergeAndWriteMetadata(stor storage.Storage, targetTools coretools.List, resolve ResolveFlag) error {
	existing, err := ReadMetadata(stor)
	if err != nil {
		return err
	}
	metadata := MetadataFromTools(targetTools)
	metadata = MergeMetadata(metadata, existing)
	if resolve {
		if err = ResolveMetadata(stor, metadata); err != nil {
			return err
		}
	}
	return WriteMetadata(stor, metadata)
}

// fetchToolsHash fetches the tools from storage and calculates
// its size in bytes and computes a SHA256 hash of its contents.
func fetchToolsHash(stor storage.StorageReader, ver version.Binary) (size int64, sha256hash hash.Hash, err error) {
	r, err := storage.Get(stor, StorageName(ver))
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()
	sha256hash = sha256.New()
	size, err = io.Copy(sha256hash, r)
	return size, sha256hash, err
}
