// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package caasoperator

import (
	"github.com/juju/utils/proxy"
	"gopkg.in/juju/charm.v6"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/status"
	"github.com/juju/juju/watcher"
)

// Client provides an interface for interacting
// with the CAASOperator API. Subsets of this
// should be passed to the CAASOperator worker.
type Client interface {
	ApplicationConfigGetter
	CharmGetter
	ContainerSpecSetter
	StatusSetter
	ModelName() (string, error)
}

// CharmGetter provides an interface for getting
// the URL and SHA256 hash of the charm currently
// assigned to the application.
type CharmGetter interface {
	Charm(application string) (_ *charm.URL, sha256 string, _ error)
}

// ContainerSpecSetter provides an interface for
// setting the container spec for the application
// or unit thereof.
type ContainerSpecSetter interface {
	SetContainerSpec(entityName, spec string) error
}

// StatusSetter provides an interface for setting
// the status of a CAAS application.
type StatusSetter interface {
	// SetStatus sets the status of an application.
	SetStatus(
		application string,
		status status.Status,
		info string,
		data map[string]interface{},
	) error
}

// ApplicationConfigGetter provides an interface for
// watching and getting the application's config settings.
type ApplicationConfigGetter interface {
	ApplicationConfig(string) (charm.Settings, error)
	WatchApplicationConfig(string) (watcher.NotifyWatcher, error)
}

// TODO(caas) - split this up
type hookAPIAdaptor struct {
	StatusSetter
	ApplicationConfigGetter
	ContainerSpecSetter

	appName string

	dummyHookAPI
}

func (h *hookAPIAdaptor) ApplicationConfig() (charm.Settings, error) {
	return h.ApplicationConfigGetter.ApplicationConfig(h.appName)
}

// dummyHookAPI is an API placeholder
type dummyHookAPI struct{}

func (h *dummyHookAPI) ApplicationStatus(appName string) (params.ApplicationStatusResult, error) {
	return params.ApplicationStatusResult{Application: params.StatusResult{Status: "unknown"}}, nil
}

func (h *dummyHookAPI) SetApplicationStatus(string, status.Status, string, map[string]interface{}) error {
	return nil
}

func (h *dummyHookAPI) NetworkInfo(bindings []string, relId *int) (map[string]params.NetworkInfoResult, error) {
	return make(map[string]params.NetworkInfoResult), nil
}

// TODO(caas) implement this API
type dummyContextFactoryAPI struct{}

func (c *dummyContextFactoryAPI) APIAddresses() ([]string, error) {
	return []string{}, nil
}

func (c *dummyContextFactoryAPI) ProxySettings() (proxy.Settings, error) {
	return proxy.Settings{}, nil
}
