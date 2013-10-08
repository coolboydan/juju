// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package upgrader_test

import (
	"fmt"
	"os"
	"path/filepath"
	stdtesting "testing"
	"time"

	gc "launchpad.net/gocheck"

	"launchpad.net/juju-core/agent"
	agenttools "launchpad.net/juju-core/agent/tools"
	envtesting "launchpad.net/juju-core/environs/testing"
	envtools "launchpad.net/juju-core/environs/tools"
	"launchpad.net/juju-core/errors"
	jujutesting "launchpad.net/juju-core/juju/testing"
	"launchpad.net/juju-core/provider/dummy"
	"launchpad.net/juju-core/state"
	"launchpad.net/juju-core/state/api"
	statetesting "launchpad.net/juju-core/state/testing"
	coretesting "launchpad.net/juju-core/testing"
	jc "launchpad.net/juju-core/testing/checkers"
	coretools "launchpad.net/juju-core/tools"
	"launchpad.net/juju-core/version"
	"launchpad.net/juju-core/worker/upgrader"
)

func TestPackage(t *stdtesting.T) {
	coretesting.MgoTestPackage(t)
}

type UpgraderSuite struct {
	jujutesting.JujuConnSuite

	machine       *state.Machine
	state         *api.State
	oldRetryAfter func() <-chan time.Time
}

var _ = gc.Suite(&UpgraderSuite{})

func (s *UpgraderSuite) SetUpTest(c *gc.C) {
	s.JujuConnSuite.SetUpTest(c)
	s.state, s.machine = s.OpenAPIAsNewMachine(c)
	// Capture the value of RetryAfter, and use that captured
	// value in the cleanup lambda.
	oldRetryAfter := *upgrader.RetryAfter
	s.AddCleanup(func(*gc.C) {
		*upgrader.RetryAfter = oldRetryAfter
	})
}

type mockConfig struct {
	agent.Config
	tag     string
	datadir string
}

func (mock *mockConfig) Tag() string {
	return mock.tag
}

func (mock *mockConfig) DataDir() string {
	return mock.datadir
}

func agentConfig(tag, datadir string) agent.Config {
	return &mockConfig{tag: tag, datadir: datadir}
}

func (s *UpgraderSuite) makeUpgrader() *upgrader.Upgrader {
	config := agentConfig(s.machine.Tag(), s.DataDir())
	return upgrader.NewUpgrader(s.state.Upgrader(), config)
}

func (s *UpgraderSuite) TestUpgraderSetsTools(c *gc.C) {
	vers := version.MustParseBinary("5.4.3-precise-amd64")
	err := statetesting.SetAgentVersion(s.State, vers.Number)
	c.Assert(err, gc.IsNil)
	stor := s.Conn.Environ.Storage()
	agentTools := envtesting.PrimeTools(c, stor, s.DataDir(), vers)
	err = envtools.MergeAndWriteMetadata(stor, coretools.List{agentTools}, envtools.Resolve)
	_, err = s.machine.AgentTools()
	c.Assert(err, jc.Satisfies, errors.IsNotFoundError)

	u := s.makeUpgrader()
	statetesting.AssertStop(c, u)
	s.machine.Refresh()
	gotTools, err := s.machine.AgentTools()
	c.Assert(err, gc.IsNil)
	envtesting.CheckTools(c, gotTools, agentTools)
}

func (s *UpgraderSuite) TestUpgraderSetVersion(c *gc.C) {
	vers := version.MustParseBinary("5.4.3-precise-amd64")
	envtesting.PrimeTools(c, s.Conn.Environ.Storage(), s.DataDir(), vers)
	err := os.RemoveAll(filepath.Join(s.DataDir(), "tools"))
	c.Assert(err, gc.IsNil)

	_, err = s.machine.AgentTools()
	c.Assert(err, jc.Satisfies, errors.IsNotFoundError)
	err = statetesting.SetAgentVersion(s.State, vers.Number)
	c.Assert(err, gc.IsNil)

	u := s.makeUpgrader()
	statetesting.AssertStop(c, u)
	s.machine.Refresh()
	gotTools, err := s.machine.AgentTools()
	c.Assert(err, gc.IsNil)
	c.Assert(gotTools, gc.DeepEquals, &coretools.Tools{Version: version.Current})
}

func (s *UpgraderSuite) TestUpgraderUpgradesImmediately(c *gc.C) {
	stor := s.Conn.Environ.Storage()
	oldTools := envtesting.PrimeTools(c, stor, s.DataDir(), version.MustParseBinary("5.4.3-precise-amd64"))
	newTools := envtesting.AssertUploadFakeToolsVersions(
		c, stor, version.MustParseBinary("5.4.5-precise-amd64"))[0]
	err := envtools.MergeAndWriteMetadata(stor, coretools.List{oldTools, newTools}, envtools.Resolve)
	c.Assert(err, gc.IsNil)
	err = statetesting.SetAgentVersion(s.State, newTools.Version.Number)
	c.Assert(err, gc.IsNil)

	// Make the download take a while so that we verify that
	// the download happens before the upgrader checks if
	// it's been stopped.
	dummy.SetStorageDelay(coretesting.ShortWait)

	u := s.makeUpgrader()
	err = u.Stop()
	envtesting.CheckUpgraderReadyError(c, err, &upgrader.UpgradeReadyError{
		AgentName: s.machine.Tag(),
		OldTools:  oldTools,
		NewTools:  newTools,
		DataDir:   s.DataDir(),
	})
	foundTools, err := agenttools.ReadTools(s.DataDir(), newTools.Version)
	c.Assert(err, gc.IsNil)
	envtesting.CheckTools(c, foundTools, newTools)
}

func (s *UpgraderSuite) TestUpgraderRetryAndChanged(c *gc.C) {
	stor := s.Conn.Environ.Storage()
	oldTools := envtesting.PrimeTools(c, stor, s.DataDir(), version.MustParseBinary("5.4.3-precise-amd64"))
	newTools := envtesting.AssertUploadFakeToolsVersions(
		c, stor, version.MustParseBinary("5.4.5-precise-amd64"))[0]
	err := envtools.MergeAndWriteMetadata(stor, coretools.List{oldTools, newTools}, envtools.Resolve)
	c.Assert(err, gc.IsNil)
	err = statetesting.SetAgentVersion(s.State, newTools.Version.Number)
	c.Assert(err, gc.IsNil)

	retryc := make(chan time.Time)
	*upgrader.RetryAfter = func() <-chan time.Time {
		c.Logf("replacement retry after")
		return retryc
	}
	dummy.Poison(s.Conn.Environ.Storage(), envtools.StorageName(newTools.Version), fmt.Errorf("a non-fatal dose"))
	u := s.makeUpgrader()
	defer u.Stop()

	for i := 0; i < 3; i++ {
		select {
		case retryc <- time.Now():
		case <-time.After(coretesting.LongWait):
			c.Fatalf("upgrader did not retry (attempt %d)", i)
		}
	}

	// Make it upgrade to some newer tools that can be
	// downloaded ok; it should stop retrying, download
	// the newer tools and exit.
	newerTools := envtesting.AssertUploadFakeToolsVersions(
		c, s.Conn.Environ.Storage(), version.MustParseBinary("5.4.6-precise-amd64"))[0]

	err = statetesting.SetAgentVersion(s.State, newerTools.Version.Number)
	c.Assert(err, gc.IsNil)

	s.BackingState.StartSync()
	done := make(chan error)
	go func() {
		done <- u.Wait()
	}()
	select {
	case err := <-done:
		envtesting.CheckUpgraderReadyError(c, err, &upgrader.UpgradeReadyError{
			AgentName: s.machine.Tag(),
			OldTools:  oldTools,
			NewTools:  newerTools,
			DataDir:   s.DataDir(),
		})
	case <-time.After(coretesting.LongWait):
		c.Fatalf("upgrader did not quit after upgrading")
	}
}

func (s *UpgraderSuite) TestChangeAgentTools(c *gc.C) {
	oldTools := &coretools.Tools{
		Version: version.MustParseBinary("1.2.3-quantal-amd64"),
	}
	stor := s.Conn.Environ.Storage()
	newTools := envtesting.PrimeTools(c, stor, s.DataDir(), version.MustParseBinary("5.4.3-precise-amd64"))
	err := envtools.MergeAndWriteMetadata(stor, coretools.List{newTools}, envtools.Resolve)
	c.Assert(err, gc.IsNil)
	ugErr := &upgrader.UpgradeReadyError{
		AgentName: "anAgent",
		OldTools:  oldTools,
		NewTools:  newTools,
		DataDir:   s.DataDir(),
	}
	err = ugErr.ChangeAgentTools()
	c.Assert(err, gc.IsNil)
	link, err := os.Readlink(agenttools.ToolsDir(s.DataDir(), "anAgent"))
	c.Assert(err, gc.IsNil)
	c.Assert(link, gc.Equals, newTools.Version.String())
}
