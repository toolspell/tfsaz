package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// setup brings a Mac from nothing to a working test bed for one OS: host
// tools, services and base VM, then verify. Steps already done are skipped,
// so it is safe to rerun.
func (e *env) setup(osName string) error {
	o, err := lookupOS(osName)
	if err != nil {
		return err
	}
	step("checking host tools")
	if err := e.doctor(true, false); err != nil {
		return err
	}
	if err := e.services("up"); err != nil {
		return err
	}
	exists, err := e.vms(o).exists(o.base())
	if err != nil {
		return err
	}
	if exists {
		step("%s exists; %s", o.base(), baseHint(o))
	} else if err := e.base("build", o.name, false); err != nil {
		return err
	}
	return e.verify(o.name)
}

// verify boots a throwaway clone and checks what live tests rely on: the
// mock by name, the router in both directions, and internet access.
func (e *env) verify(osName string) (err error) {
	if err := requireMac(); err != nil {
		return err
	}
	o, err := lookupOS(osName)
	if err != nil {
		return err
	}
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	info, err := e.vmUp(osName, "verify-"+hex.EncodeToString(b))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, e.vmDown(info.Name)) }()

	pat := os.Getenv("MOCK_PAT")
	if pat == "" {
		pat = "mock-pat"
	}
	checks := []struct{ name, command string }{
		{"the VM resolves devops.mock.test to the mock", o.guest.resolves},
		{"the mock's REST API answers through the router", fmt.Sprintf(o.guest.rest, pat)},
		{"traffic to the internet goes through the tunnel", o.guest.tunneled},
		{"the VM reaches the internet", o.guest.internet},
	}
	if o.guest.session != "" {
		checks = append(checks, struct{ name, command string }{"the test user is logged in to the GUI", o.guest.session})
	}
	failed := 0
	login := info.login()
	for _, c := range checks {
		_, err := login.run(nil, c.command)
		failed += report(c.name, err)
	}
	failed += report("the test bed reaches the VM",
		e.compose("exec", "-T", "router", "nc", "-z", "-w", "3", info.BedAddress, "22"))
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed", failed, len(checks)+1)
	}
	step("%s works", osName)
	return nil
}

func report(name string, err error) int {
	if err != nil {
		fmt.Printf("FAIL  %s: %v\n", name, err)
		return 1
	}
	fmt.Printf("ok    %s\n", name)
	return 0
}
