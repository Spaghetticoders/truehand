// Package evidence records what hands produced, and on which content they produced it.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spaghetticoders/truehand/internal/board"
	"github.com/spaghetticoders/truehand/internal/gitx"
)

const (
	Pass  = "pass"
	Fail  = "fail"
	Error = "error"
)

type File struct {
	Path   string `json:"path"` // relative to the task directory
	SHA256 string `json:"sha256"`
}

// Fingerprint identifies the content a hand ran on: the blob hash of every file the task
// changes relative to its base. It is portable to CI because it is recomputed from the
// checkout, not from local git objects.
type Fingerprint struct {
	Digest string            `json:"digest"`
	Files  map[string]string `json:"files"`
}

type Record struct {
	ID      string         `json:"id"`
	Hand    string         `json:"hand"`
	Key     string         `json:"key,omitempty"`
	Verdict string         `json:"verdict"`
	Summary string         `json:"summary"`
	Actor   string         `json:"actor,omitempty"`
	Files   []File         `json:"files,omitempty"`
	Inputs  Fingerprint    `json:"inputs"`
	Created time.Time      `json:"created"`
	Data    map[string]any `json:"data,omitempty"`
}

// ExcludedPaths never count as task content: Truehand's own state would otherwise change
// the fingerprint every time evidence is written.
var ExcludedPaths = []string{".truehand"}

func Compute(repo *gitx.Repo, base string) (Fingerprint, error) {
	files, err := repo.ChangedFiles(base, ExcludedPaths...)
	if err != nil {
		return Fingerprint{}, err
	}
	hashes, err := repo.HashFiles(files)
	if err != nil {
		return Fingerprint{}, err
	}
	return Fingerprint{Digest: digest(hashes), Files: hashes}, nil
}

func digest(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", k, files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Stale lists the files whose content differs between the record's inputs and now.
func (f Fingerprint) Stale(now Fingerprint) []string {
	if f.Digest == now.Digest {
		return nil
	}
	var diff []string
	for p, h := range now.Files {
		if f.Files[p] != h {
			diff = append(diff, p)
		}
	}
	for p := range f.Files {
		if _, ok := now.Files[p]; !ok {
			diff = append(diff, p)
		}
	}
	sort.Strings(diff)
	if len(diff) == 0 {
		diff = []string{"(fingerprint changed)"}
	}
	return diff
}

type Log struct {
	Store *board.Store
	Task  string
}

func (l Log) Dir() string { return filepath.Join(l.Store.TaskDir(l.Task), "evidence") }

func (l Log) indexPath() string { return filepath.Join(l.Dir(), "index.jsonl") }

func (l Log) Records() ([]Record, error) {
	var out []Record
	err := board.ReadJSONL(l.indexPath(), func(line []byte) error {
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return err
		}
		out = append(out, r)
		return nil
	})
	return out, err
}

// NewID reserves the next record id for a hand, e.g. "test-proof-3". The reservation is a
// directory created exclusively, so hands running in parallel never share an id.
func (l Log) NewID(hand string) (string, error) {
	dir := filepath.Join(l.Dir(), ".ids")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for n := 1; n < 100000; n++ {
		id := fmt.Sprintf("%s-%d", hand, n)
		err := os.Mkdir(filepath.Join(dir, id), 0o755)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("too many %s records", hand)
}

// FilePath returns the absolute path for an evidence file of a record, and its task-relative path.
func (l Log) FilePath(recordID, name string) (abs, rel string) {
	rel = "evidence/" + recordID + "-" + name
	return filepath.Join(l.Store.TaskDir(l.Task), filepath.FromSlash(rel)), rel
}

// Append hashes the record's files and appends it to the index.
func (l Log) Append(r *Record) error {
	if r.Created.IsZero() {
		r.Created = time.Now().UTC()
	}
	for i := range r.Files {
		sum, err := hashFile(filepath.Join(l.Store.TaskDir(l.Task), filepath.FromSlash(r.Files[i].Path)))
		if err != nil {
			return err
		}
		r.Files[i].SHA256 = sum
	}
	if err := os.MkdirAll(l.Dir(), 0o755); err != nil {
		return err
	}
	return board.AppendJSONL(l.indexPath(), r)
}

// Integrity checks that every file of a record still exists with the recorded hash.
func (l Log) Integrity(r Record) error {
	for _, f := range r.Files {
		if strings.Contains(f.Path, "..") {
			return fmt.Errorf("%s: invalid path %q", r.ID, f.Path)
		}
		sum, err := hashFile(filepath.Join(l.Store.TaskDir(l.Task), filepath.FromSlash(f.Path)))
		if err != nil {
			return fmt.Errorf("%s: %w", r.ID, err)
		}
		if sum != f.SHA256 {
			return fmt.Errorf("%s: %s was modified after it was recorded", r.ID, f.Path)
		}
	}
	return nil
}

// Latest returns the most recent record per key for a hand (key "" for hands without keys).
func Latest(recs []Record, hand string) map[string]Record {
	out := map[string]Record{}
	for _, r := range recs {
		if r.Hand == hand {
			out[r.Key] = r
		}
	}
	return out
}

// LatestAll returns the most recent record per (hand, key).
func LatestAll(recs []Record) []Record {
	idx := map[string]int{}
	var out []Record
	for _, r := range recs {
		k := r.Hand + "\x00" + r.Key
		if i, ok := idx[k]; ok {
			out[i] = r
			continue
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	return out
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashBytes is the sha256 hex digest used across Truehand.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
