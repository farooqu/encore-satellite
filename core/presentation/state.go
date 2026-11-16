// Package presentation contains snapshots only, not dashboard or timer logic.
package presentation

type State struct {
	Backend   string `json:"backend"`
	Connected bool   `json:"connected"`
	Muted     bool   `json:"muted"`
	Phase     string `json:"phase"`
	Heard     string `json:"heard,omitempty"`
	Reply     string `json:"reply,omitempty"`
	Alert     bool   `json:"alert"`
	Reason    string `json:"reason,omitempty"`
}
