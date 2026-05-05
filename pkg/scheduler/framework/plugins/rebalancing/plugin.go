/*
Copyright 2026 The KubeFleet Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rebalancing

import (
	"github.com/kubefleet-dev/kubefleet/pkg/scheduler/framework"
)

// Plugin is the scheduler plugin that implements the support for rebalancing requests
// in the scheduling cycle.
type Plugin struct {
	// The name of the plugin.
	name string

	// The framework handle.
	handle framework.Handle
}

var (
	// Verify that Plugin can connect to relevant extension points at compile time.
	//
	// This plugin leverages the following the extension points:
	// * Filter
	//
	// Note that successful connection to any of the extension points implies that the
	// plugin already implements the Plugin interface.
	_ framework.FilterPlugin = &Plugin{}
)

type rebalancingPluginOptions struct {
	// The name of the plugin.
	name string
}

type Option func(*rebalancingPluginOptions)

var defaultPluginOptions = rebalancingPluginOptions{
	name: "Rebalancing",
}

// WithName sets the name of the plugin.
func WithName(name string) Option {
	return func(opts *rebalancingPluginOptions) {
		opts.name = name
	}
}

// New initializes and returns a new plugin with the given options.
func New(opts ...Option) Plugin {
	options := defaultPluginOptions
	for _, opt := range opts {
		opt(&options)
	}

	return Plugin{
		name: options.name,
	}
}

// Name returns the name of the plugin.
func (p *Plugin) Name() string {
	return p.name
}

// SetUpWithFramework sets up this plugin with a scheduler framework.
func (p *Plugin) SetUpWithFramework(handle framework.Handle) {
	p.handle = handle
}
