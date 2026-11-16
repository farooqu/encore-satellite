// Package wake carries evidence from a host's wake executor. No executor or
// trained model is bundled in the first increment.
package wake

import "errors"

// Detection retains the protocol-2 model identity and 0..255 scores verbatim.
// Incubator receives every candidate; Encore must not pre-elect its winner.
type Detection struct {
	Word           string `json:"wake_word"`
	Probability    uint8  `json:"probability"`
	MaxProbability uint8  `json:"max_probability"`
	Cutoff         uint8  `json:"cutoff"`
}

func (d Detection) Validate() error {
	if d.Word == "" || len(d.Word) > 128 || d.Word == "manual" || d.Word == "follow_up" || d.Word == "stop" {
		return errors.New("wake: expected a model identity, not a reserved start reason")
	}
	return nil
}
