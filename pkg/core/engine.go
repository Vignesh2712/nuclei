package core

import (
	"github.com/projectdiscovery/nuclei/v3/pkg/output"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols"
	"github.com/projectdiscovery/nuclei/v3/pkg/types"
)

// Engine is an executer for running Nuclei Templates/Workflows.
//
// The engine contains multiple thread pools which allow using different
// concurrency values per protocol executed.
//
// The engine does most of the heavy lifting of execution, from clustering
// templates to leading to the final execution by the work pool, it is
// handled by the engine.
type Engine struct {
	workPool     *WorkPool
	options      *types.Options
	executerOpts protocols.ExecutorOptions
	Callback     func(*output.ResultEvent) // Executed on results
}

// New returns a new Engine instance
func New(options *types.Options) *Engine {
	engine := &Engine{
		options: options,
	}
	return engine
}

// GetWorkPool returns a workpool from options
func (e *Engine) GetWorkPool() *WorkPool {
	return NewWorkPool(WorkPoolConfig{
		InputConcurrency:         e.executerOpts.CruiseControl.Standard().Concurrency.Hosts,
		TypeConcurrency:          e.executerOpts.CruiseControl.Standard().Concurrency.Templates,
		HeadlessInputConcurrency: e.executerOpts.CruiseControl.Headless().Concurrency.Hosts,
		HeadlessTypeConcurrency:  e.executerOpts.CruiseControl.Headless().Concurrency.Templates,
	})
}

// SetExecuterOptions sets the executer options for the engine. This is required
// before using the engine to perform any execution.
func (e *Engine) SetExecuterOptions(options protocols.ExecutorOptions) {
	e.executerOpts = options
	e.workPool = e.GetWorkPool()
}

// ExecuterOptions returns protocols.ExecutorOptions for nuclei engine.
func (e *Engine) ExecuterOptions() protocols.ExecutorOptions {
	return e.executerOpts
}

// WorkPool returns the worker pool for the engine
func (e *Engine) WorkPool() *WorkPool {
	return e.workPool
}
