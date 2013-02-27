package openstack_test

import (
	"fmt"
	. "launchpad.net/gocheck"
	"launchpad.net/goose/identity"
	"launchpad.net/goose/testservices/hook"
	"launchpad.net/goose/testservices/openstackservice"
	"launchpad.net/juju-core/environs"
	"launchpad.net/juju-core/environs/jujutest"
	"launchpad.net/juju-core/environs/openstack"
	"launchpad.net/juju-core/juju/testing"
	"launchpad.net/juju-core/state"
	coretesting "launchpad.net/juju-core/testing"
	"net/http"
	"net/http/httptest"
)

type ProviderSuite struct{}

var _ = Suite(&ProviderSuite{})

func (s *ProviderSuite) TestMetadata(c *C) {
	openstack.UseTestMetadata(true)
	defer openstack.UseTestMetadata(false)

	p, err := environs.Provider("openstack")
	c.Assert(err, IsNil)

	addr, err := p.PublicAddress()
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, "public.dummy.address.example.com")

	addr, err = p.PrivateAddress()
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, "private.dummy.address.example.com")

	id, err := p.InstanceId()
	c.Assert(err, IsNil)
	c.Assert(id, Equals, state.InstanceId("d8e02d56-2648-49a3-bf97-6be8f1204f38"))
}

// Register tests to run against a test Openstack instance (service doubles).
func registerLocalTests() {
	cred := &identity.Credentials{
		User:       "fred",
		Secrets:    "secret",
		Region:     "some region",
		TenantName: "some tenant",
	}
	Suite(&localLiveSuite{
		LiveTests: LiveTests{
			cred: cred,
		},
	})
	Suite(&localServerSuite{
		cred: cred,
	})
}

// localServer is used to spin up a local Openstack service double.
type localServer struct {
	Server     *httptest.Server
	Mux        *http.ServeMux
	oldHandler http.Handler
	Service    *openstackservice.Openstack
}

func (s *localServer) start(c *C, cred *identity.Credentials) {
	// Set up the HTTP server.
	s.Server = httptest.NewServer(nil)
	s.oldHandler = s.Server.Config.Handler
	s.Mux = http.NewServeMux()
	s.Server.Config.Handler = s.Mux
	cred.URL = s.Server.URL
	s.Service = openstackservice.New(cred)
	s.Service.SetupHTTP(s.Mux)
	openstack.ShortTimeouts(true)
}

func (s *localServer) stop() {
	s.Mux = nil
	s.Server.Config.Handler = s.oldHandler
	s.Server.Close()
	openstack.ShortTimeouts(false)
}

// localLiveSuite runs tests from LiveTests using an Openstack service double.
type localLiveSuite struct {
	coretesting.LoggingSuite
	LiveTests
	srv localServer
}

// localServerSuite contains tests that run against an Openstack service double.
// These tests can test things that would be unreasonably slow or expensive
// to test on a live Openstack server. The service double is started and stopped for
// each test.
type localServerSuite struct {
	coretesting.LoggingSuite
	jujutest.Tests
	cred *identity.Credentials
	srv  localServer
}

func (s *localLiveSuite) SetUpSuite(c *C) {
	s.LoggingSuite.SetUpSuite(c)
	c.Logf("Running live tests using openstack service test double")

	s.srv.start(c, s.cred)
	s.LiveTests.SetUpSuite(c)
}

func (s *localLiveSuite) TearDownSuite(c *C) {
	s.LiveTests.TearDownSuite(c)
	s.srv.stop()
	s.LoggingSuite.TearDownSuite(c)
}

func (s *localLiveSuite) SetUpTest(c *C) {
	s.LoggingSuite.SetUpTest(c)
	s.LiveTests.SetUpTest(c)
}

func (s *localLiveSuite) TearDownTest(c *C) {
	s.LiveTests.TearDownTest(c)
	s.LoggingSuite.TearDownTest(c)
}

func (s *localServerSuite) SetUpSuite(c *C) {
	s.LoggingSuite.SetUpSuite(c)
	s.Tests.SetUpSuite(c)
	c.Logf("Running local tests")
}

func (s *localServerSuite) TearDownSuite(c *C) {
	s.Tests.TearDownSuite(c)
	s.LoggingSuite.TearDownSuite(c)
}

func testConfig(cred *identity.Credentials) map[string]interface{} {
	attrs := makeTestConfig()
	attrs["admin-secret"] = "secret"
	attrs["username"] = cred.User
	attrs["password"] = cred.Secrets
	attrs["region"] = cred.Region
	attrs["auth-url"] = cred.URL
	attrs["tenant-name"] = cred.TenantName
	attrs["default-image-id"] = testImageId
	return attrs
}

func (s *localServerSuite) SetUpTest(c *C) {
	s.LoggingSuite.SetUpTest(c)
	s.srv.start(c, s.cred)
	s.Tests = jujutest.Tests{
		Config: testConfig(s.cred),
	}
	s.Tests.SetUpTest(c)
	writeablePublicStorage := openstack.WritablePublicStorage(s.Env)
	putFakeTools(c, writeablePublicStorage)
}

func (s *localServerSuite) TearDownTest(c *C) {
	s.Tests.TearDownTest(c)
	s.srv.stop()
	s.LoggingSuite.TearDownTest(c)
}

func panicWrite(name string, cert, key []byte) error {
	panic("writeCertAndKey called unexpectedly")
}

// If the bootstrap node is configured to require a public IP address (the default),
// bootstrapping fails if an address cannot be allocated.
func (s *localLiveSuite) TestBootstrapFailsWhenPublicIPError(c *C) {
	cleanup := s.srv.Service.Nova.RegisterControlPoint(
		"addFloatingIP",
		func(sc hook.ServiceControl, args ...interface{}) error {
			return fmt.Errorf("failed on purpose")
		},
	)
	defer cleanup()
	err := environs.Bootstrap(s.Env, true, panicWrite)
	c.Assert(err, ErrorMatches, ".*cannot allocate a public IP as needed.*")
}

// If the environment is configured not to require a public IP address for nodes,
// bootstrapping and starting an instance should occur without any attempt to allocate a public address.
func (s *localServerSuite) TestStartInstanceWithoutPublicIP(c *C) {
	openstack.SetUseFloatingIP(s.Env, false)
	cleanup := s.srv.Service.Nova.RegisterControlPoint(
		"addFloatingIP",
		func(sc hook.ServiceControl, args ...interface{}) error {
			return fmt.Errorf("add floating IP should not have been called")
		},
	)
	defer cleanup()
	cleanup = s.srv.Service.Nova.RegisterControlPoint(
		"addServerFloatingIP",
		func(sc hook.ServiceControl, args ...interface{}) error {
			return fmt.Errorf("add server floating IP should not have been called")
		},
	)
	defer cleanup()
	err := environs.Bootstrap(s.Env, true, panicWrite)
	c.Assert(err, IsNil)
	inst, err := s.Env.StartInstance("100", testing.InvalidStateInfo("100"), testing.InvalidAPIInfo("100"), nil)
	c.Assert(err, IsNil)
	err = s.Env.StopInstances([]environs.Instance{inst})
	c.Assert(err, IsNil)
}

var instanceGathering = []struct {
	ids []state.InstanceId
	err error
}{
	{ids: []state.InstanceId{"id0"}},
	{ids: []state.InstanceId{"id0", "id0"}},
	{ids: []state.InstanceId{"id0", "id1"}},
	{ids: []state.InstanceId{"id1", "id0"}},
	{ids: []state.InstanceId{"id1", "id0", "id1"}},
	{
		ids: []state.InstanceId{""},
		err: environs.ErrNoInstances,
	},
	{
		ids: []state.InstanceId{"", ""},
		err: environs.ErrNoInstances,
	},
	{
		ids: []state.InstanceId{"", "", ""},
		err: environs.ErrNoInstances,
	},
	{
		ids: []state.InstanceId{"id0", ""},
		err: environs.ErrPartialInstances,
	},
	{
		ids: []state.InstanceId{"", "id1"},
		err: environs.ErrPartialInstances,
	},
	{
		ids: []state.InstanceId{"id0", "id1", ""},
		err: environs.ErrPartialInstances,
	},
	{
		ids: []state.InstanceId{"id0", "", "id0"},
		err: environs.ErrPartialInstances,
	},
	{
		ids: []state.InstanceId{"id0", "id0", ""},
		err: environs.ErrPartialInstances,
	},
	{
		ids: []state.InstanceId{"", "id0", "id1"},
		err: environs.ErrPartialInstances,
	},
}

func (s *localServerSuite) TestInstancesGathering(c *C) {
	inst0, err := s.Env.StartInstance("100", testing.InvalidStateInfo("100"), testing.InvalidAPIInfo("100"), nil)
	c.Assert(err, IsNil)
	id0 := inst0.Id()
	inst1, err := s.Env.StartInstance("101", testing.InvalidStateInfo("101"), testing.InvalidAPIInfo("101"), nil)
	c.Assert(err, IsNil)
	id1 := inst1.Id()
	defer func() {
		err := s.Env.StopInstances([]environs.Instance{inst0, inst1})
		c.Assert(err, IsNil)
	}()

	for i, test := range instanceGathering {
		c.Logf("test %d: find %v -> expect len %d, err: %v", i, test.ids, len(test.ids), test.err)
		ids := make([]state.InstanceId, len(test.ids))
		for j, id := range test.ids {
			switch id {
			case "id0":
				ids[j] = id0
			case "id1":
				ids[j] = id1
			}
		}
		insts, err := s.Env.Instances(ids)
		c.Assert(err, Equals, test.err)
		if err == environs.ErrNoInstances {
			c.Assert(insts, HasLen, 0)
		} else {
			c.Assert(insts, HasLen, len(test.ids))
		}
		for j, inst := range insts {
			if ids[j] != "" {
				c.Assert(inst.Id(), Equals, ids[j])
			} else {
				c.Assert(inst, IsNil)
			}
		}
	}
}
