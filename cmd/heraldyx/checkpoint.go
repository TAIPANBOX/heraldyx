package main

import (
	"github.com/TAIPANBOX/heraldyx/internal/checkpoint"
	"github.com/TAIPANBOX/heraldyx/internal/config"
)

// checkpointGate builds the coverage gate the configuration asks for, or nil
// when the gate is off, which is the default. A gate that is on and cannot load
// its keys does not start: the error names the file.
func checkpointGate(cfg config.Config) (*checkpoint.Gate, error) {
	if !cfg.CheckpointGate {
		return nil, nil
	}
	keys, err := checkpoint.LoadKeys(cfg.CheckpointKeys)
	if err != nil {
		return nil, err
	}
	return checkpoint.NewGate(cfg.CheckpointDir, keys), nil
}
