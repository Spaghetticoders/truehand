package board

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"
)

// Approval is a human sign-off on a task's plan. It binds to the plan's hash, so editing the
// plan after approval voids it.
type Approval struct {
	Time    time.Time `json:"time"`
	State   State     `json:"state"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	PlanSHA string    `json:"plan_sha256"`
	Via     string    `json:"via"`
	Nonce   string    `json:"nonce"`
}

func (s *Store) AppendApproval(id string, a Approval) error {
	if a.Time.IsZero() {
		a.Time = time.Now().UTC()
	}
	if a.Nonce == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		a.Nonce = hex.EncodeToString(b)
	}
	return AppendJSONL(filepath.Join(s.TaskDir(id), "approvals.jsonl"), a)
}

func (s *Store) Approvals(id string) ([]Approval, error) {
	var out []Approval
	err := ReadJSONL(filepath.Join(s.TaskDir(id), "approvals.jsonl"), func(line []byte) error {
		var a Approval
		if err := json.Unmarshal(line, &a); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	return out, err
}

func (s *Store) PlanPath(id string) string { return filepath.Join(s.TaskDir(id), "plan.md") }
