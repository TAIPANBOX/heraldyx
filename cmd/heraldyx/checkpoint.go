package main

import (
	"context"
	"log"
	"time"

	"github.com/TAIPANBOX/heraldyx/internal/checkpoint"
	"github.com/TAIPANBOX/heraldyx/internal/config"
	"github.com/TAIPANBOX/heraldyx/internal/keystatement"
)

// checkpointGate builds the coverage gate the configuration asks for, or nil
// when the gate is off, which is the default. In the pinned mode a gate that
// cannot load its keys does not start: the error names the file. In the
// custody mode the keys come from custody's key statements, through a
// resolver whose own loop asks custody; run starts that loop and is nil in the
// pinned mode.
func checkpointGate(cfg config.Config) (gate *checkpoint.Gate, run func(context.Context), err error) {
	if !cfg.CheckpointGate {
		return nil, nil, nil
	}
	if cfg.CheckpointStatements != "" {
		r := keystatement.NewResolver(cfg.CheckpointStatements, cfg.CheckpointRootPin, cfg.CheckpointStreamKeys, time.Now, log.Printf)
		return checkpoint.NewGateFrom(cfg.CheckpointDir, r), r.Run, nil
	}
	keys, err := checkpoint.LoadKeys(cfg.CheckpointKeys)
	if err != nil {
		return nil, nil, err
	}
	return checkpoint.NewGate(cfg.CheckpointDir, keys), nil, nil
}
