package google

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"sync"

	gdrive "google.golang.org/api/drive/v3"

	"github.com/asjoyner/shade"
	"github.com/asjoyner/shade/drive"

	"golang.org/x/net/context"
)

func init() {
	drive.RegisterProvider("google", NewClient)
}

// NewClient returns a new Drive client.
func NewClient(c drive.Config) (drive.Client, error) {
	client := getOAuthClient(c)
	service, err := gdrive.New(client)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve Google Drive Client: %v", err)
	}
	return &Drive{
		service: service,
		config:  c,
		files:   make(map[string]string),
	}, nil
}

// Drive represents access to the Google Drive storage system.
type Drive struct {
	service *gdrive.Service
	config  drive.Config

	mu    sync.RWMutex // protects following members
	files map[string]string
}

// ListFiles retrieves all of the File objects known to the client, and returns
// the corresponding sha256sum of the file object.  Those may be passed to
// GetChunk() to retrieve the corresponding shade.File.
func (s *Drive) ListFiles() ([][]byte, error) {
	ctx := context.TODO() // TODO(cfunkhouser): Get a meaningful context here.
	// This query is a Google Drive API query string which will return all
	// shade metadata files. If FileParentID is specified, the query is restricted
	// there and space "drive" is used; otherwise, space "appDataFolder" is used.
	q := "appProperties has { key='shadeType' and value='file' }"
	spaces := "appDataFolder"
	if s.config.FileParentID != "" {
		q = fmt.Sprintf("%s and '%s' in parents", q, s.config.FileParentID)
		spaces = "drive"
	}
	r, err := s.service.Files.List().Spaces(spaces).Context(ctx).Q(q).Fields("files(id, name)").Do()
	if err != nil {
		return nil, fmt.Errorf("couldn't retrieve files: %v", err)
	}
	s.mu.Lock()
	for _, f := range r.Files {
		// If decoding the name fails, skip the file.
		if b, err := hex.DecodeString(f.Name); err == nil {
			s.files[string(b)] = f.Id
		}
	}
	s.mu.Unlock()

	s.mu.RLock()
	defer s.mu.RUnlock()
	resp := make([][]byte, 0, len(s.files))
	for sha256sum := range s.files {
		resp = append(resp, []byte(sha256sum))
	}
	return resp, nil
}

// GetFile retrieves a chunk with a given SHA-256 sum
func (s *Drive) GetFile(sha256sum []byte) ([]byte, error) {
	return s.GetChunk(sha256sum, nil)
}

// PutFile writes the metadata describing a new file.
// content should be marshalled JSON, and may be encrypted.
func (s *Drive) PutFile(sha256sum, content []byte) error {
	f := &gdrive.File{
		Name:          hex.EncodeToString(sha256sum),
		AppProperties: map[string]string{"shadeType": "file"},
	}
	if s.config.FileParentID != "" {
		f.Parents = []string{s.config.FileParentID}
	} else {
		f.Parents = []string{"appDataFolder"}
	}

	ctx := context.TODO() // TODO(cfunkhouser): Get a meaningful context here.
	br := bytes.NewReader(content)
	if _, err := s.service.Files.Create(f).Context(ctx).Media(br).Do(); err != nil {
		return fmt.Errorf("couldn't create file: %v", err)
	}
	return nil
}

// GetChunk retrieves a chunk with a given SHA-256 sum
func (s *Drive) GetChunk(sha256sum []byte, _ *shade.File) ([]byte, error) {
	s.mu.RLock()
	fileID, ok := s.files[string(sha256sum)]
	s.mu.RUnlock()

	filename := hex.EncodeToString(sha256sum)
	if !ok {
		ctx := context.TODO() // TODO(cfunkhouser): Get a meaningful context here.
		q := fmt.Sprintf("name = '%s'", filename)
		spaces := "appDataFolder"
		if s.config.ChunkParentID != "" {
			q = fmt.Sprintf("%s and '%s' in parents", q, s.config.ChunkParentID)
			spaces = "drive"
		}
		r, err := s.service.Files.List().Spaces(spaces).Context(ctx).Q(q).Fields("files(id, name)").Do()
		if err != nil {
			return nil, fmt.Errorf("couldn't get metadata for chunk %v: %v", filename, err)
		}
		if len(r.Files) != 1 {
			return nil, fmt.Errorf("got non-unique chunk result for chunk %v: %#v", filename, r.Files)
		}
		fileID = r.Files[0].Id
	}

	resp, err := s.service.Files.Get(fileID).Download()
	if err != nil {
		return nil, fmt.Errorf("couldn't download chunk %v: %v", filename, err)
	}
	defer resp.Body.Close()

	chunk, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("couldn't read chunk %v: %v", filename, err)
	}
	return chunk, nil
}

// PutChunk writes a chunk and returns its SHA-256 sum
func (s *Drive) PutChunk(sha256sum, content []byte, _ *shade.File) error {
	s.mu.RLock()
	_, ok := s.files[string(sha256sum)]
	s.mu.RUnlock()
	if ok {
		return nil // we know this chunk already exists
	}
	f := &gdrive.File{
		Name:          hex.EncodeToString(sha256sum),
		AppProperties: map[string]string{"shadeType": "chunk"},
	}
	if s.config.ChunkParentID != "" {
		f.Parents = []string{s.config.ChunkParentID}
	} else {
		f.Parents = []string{"appDataFolder"}
	}

	ctx := context.TODO() // TODO(cfunkhouser): Get a meaningful context here.
	br := bytes.NewReader(content)
	if _, err := s.service.Files.Create(f).Context(ctx).Media(br).Do(); err != nil {
		return fmt.Errorf("couldn't create file: %v", err)
	}
	return nil
}

// GetConfig returns the associated drive.Config object.
func (s *Drive) GetConfig() drive.Config {
	return s.config
}

// Local returns whether access is local.
func (s *Drive) Local() bool { return false }

// Persistent returns whether the storage is persistent across task restarts.
func (s *Drive) Persistent() bool { return true }
